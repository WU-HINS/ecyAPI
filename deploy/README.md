# 部署编排

四种形态，按机器数量与可用性要求选：

| 文件 | 形态 | 适用 |
| --- | --- | --- |
| `docker-compose.yml` + `.env.example` | 单实例（推荐默认） | 一台机器，最省心；数据全在 `./data` |
| `compose.ha.yml` + `Caddyfile` | 1 写入实例 + 2 只读副本 + Caddy | 读多写少，想扛更多并发或做零停机重启 |
| `k8s/ecyapi.yaml` | Kubernetes 单副本 + PVC + Ingress | 已有 K8s 集群 |
| `systemd/ecyapi.service` | 裸机二进制 + systemd | 不用容器，直接跑 release 里的静态二进制 |

> 所有形态共用同一份数据布局：`data/categories/<分类路径>/` 放图片，`data/*.json` 放绑定与设置，
> `data/preview-cache/` 放 `?w=` 缩略图缓存。**必须整目录挂载 `/app/data`**，
> 只挂 `/app/data/categories` 会让绑定、设置、缓存丢失。

## 快速开始（单实例）

```bash
cd deploy
cp .env.example .env
sed -i 's/change-me-please/你的强密码/' .env
mkdir -p data/categories
docker compose up -d
curl -s localhost:8080/healthz        # ok
```

首次使用：

1. 打开 `http://<主机>:8080/admin`，用 `.env` 里的密码登录；
2. 「分类管理 → 创建新分类」，填 `anime` 或 `anime/2024`（默认按设备自动分横竖）；
3. 上传图片（横图给桌面、竖图给移动端，可混在同一个分类里）；
4. 首页卡片与 `/anime` 跳转接口立即生效。

## 多实例（读多写少）

```bash
cd deploy
cp .env.example .env && vi .env
docker compose -f compose.ha.yml up -d
```

- 入口是 Caddy，宿主端口由 `HTTP_PORT` 决定（默认 8080）；`/admin*` 固定转发到**写入实例** `ecyapi-w`，
  其余请求在 `ecyapi-a` / `ecyapi-b` 之间轮询；
- 副本以 `./data:ro` 挂载图片与 JSON，缩略图缓存写到各自的命名卷（覆盖只读挂载的那一层）；
- **改完绑定/设置后要重启副本**：`endpoints.json`、`settings.json` 只在进程启动时读取；
  图片增删是 3 秒节流重扫，不用重启。

## Kubernetes

```bash
kubectl create namespace ecyapi
# 改掉 Secret 里的密码、Ingress 里的域名与 TLS secret 名
kubectl apply -f k8s/ecyapi.yaml
kubectl -n ecyapi rollout status deploy/ecyapi
```

默认 `replicas: 1` + `strategy: Recreate`：数据是单写本地目录，滚动更新会让两个 Pod 同时写同一份数据。
要扩容读副本，需要：RWX 共享存储（只读挂载图片）、把 `/admin` 固定路由到写入 Pod、
每个副本独立缓存卷——与 `compose.ha.yml` 的思路一致，K8s 侧可自行拆 Deployment。

## systemd（裸机）

```bash
sudo useradd -r -s /usr/sbin/nologin ecy
sudo mkdir -p /opt/ecyapi /var/lib/ecyapi/categories
sudo tar -xzf ecyapi-vX.Y.Z-linux-amd64.tar.gz -C /opt/ecyapi
sudo chown -R ecy:ecy /opt/ecyapi /var/lib/ecyapi
sudo install -m 600 /dev/null /etc/ecyapi.env    # 写入 ECY_ADMIN_PASSWORD 等
sudo cp systemd/ecyapi.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now ecyapi
```

二进制来自 GitHub Release（`vX.Y.Z` 为正式版，`ci-*` 镜像对应 main 的每次提交）。

## 备份与恢复

只有 `data/` 需要备份（图片 + JSON + 可重建的缓存）：

```bash
# 备份（缓存目录可排除，重启会重建）
tar -czf "ecyapi-backup-$(date +%F).tar.gz" \
  --exclude='data/preview-cache' -C ./deploy data

# 恢复
tar -xzf ecyapi-backup-2026-10-28.tar.gz -C ./deploy
docker compose restart        # 或 kubectl rollout restart
```

## 升级

| 想跟的版本 | 镜像标签 | 说明 |
| --- | --- | --- |
| 稳定版 | `latest` / `vX.Y.Z` | 由 Release 工作流推送，含二进制与校验和 |
| 最新提交 | `ci` / `ci-<sha>` | 每次 push main 自动构建，用于验证修复 |

```bash
docker compose pull && docker compose up -d
```

容器收到 SIGTERM 后会先停止接新连接、最多等 15 秒让在途请求（含上传）收尾，再退出。

## 排障

```bash
docker compose ps
docker compose logs -f --tail=100 ecyapi
curl -s localhost:8080/healthz
```

常见问题：

- **后台改完设置，副本还是旧配置** → 副本只在启动时读 JSON，重启副本即可；
- **上传报「分类名不合法」** → 分类名每段字母开头，最多 3 段，如 `anime/2024`；
- **`?w=` 没有压缩直接回原图** → 镜像未启用 CGO/libwebp（本仓库镜像默认启用），或该格式不支持预览（mp4/svg/avif）；
- **容器内是 root** → 这是为了挂载宿主空目录时不需要改属主；介意的话自行加 `user:` 并预先 `chown`。
