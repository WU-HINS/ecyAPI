# 远端后端（WebDAV / S3 / OpenList）

目标：把 OpenList（AList 系）挂载的各大网盘、或者任意 S3/MinIO 桶，变成 ecyAPI 的稳定图片后端。

## 为什么不做「远程直接索引」

现有服务端是**重本地**的：

- 索引靠 `readdir + stat` 增量比对（名字/大小/mtime）
- 出图走 `sendfile`，缩略图要本地解码，图库缓存是本地 HTML+gzip
- 内存目标是按「本地索引条目数」算的

把数据源改成远程，等于把上面每一层都重写一遍（还要处理远端限流、超时、分页、签名 URL 过期）。
风险远大于收益。

## 采用的方案：远端 → 本地镜像

```
OpenList / 网盘 / S3  ──(WebDAV / S3 API)──▶  ecyapi-mirror  ──▶  本地 data/categories
                                                    │                    │
                                              增量同步（大小/mtime）   服务端照常扫描/出图
```

- `tools/mirror` 把远端目录树增量同步到本地目录（临时文件 + rename，不会留下半个文件）
- 服务端 `ECY_DATA_DIR` 指向这个目录，**索引、缩略图、图库、内存控制全部原样复用**
- 远端变 → 下次同步 → 文件 mtime 变 → 服务端增量扫描只重建变化的那几个分类

## 用法

### OpenList（WebDAV）

```bash
# 一次性同步
ecyapi-mirror \
  -url https://openlist.example.com/dav \
  -user admin -pass '你的密码' \
  -dest /srv/ecyapi/data/categories \
  -workers 8 -max-depth 3 -prune

# 常驻：每 5 分钟同步一次（配合 systemd/docker）
ecyapi-mirror -url ... -dest ... -interval 5m
```

然后把服务端的 `ECY_DATA_DIR` 指到 `/srv/ecyapi/data/categories` 即可。

> OpenList 也支持 `-token` 传 Bearer token（部分部署关掉了 Basic Auth）。

### S3 / MinIO

```bash
ecyapi-mirror -src s3 \
  -url https://minio.example.com:9000 -region us-east-1 \
  -bucket pics -prefix library \
  -user ACCESS_KEY -pass SECRET_KEY \
  -dest /srv/ecyapi/data/categories
```

- 使用 **SigV4** 签名（`ListObjectsV2` + `GetObject`），兼容 AWS / MinIO / 各类 S3 网关
- `-path-style`（默认 true）适配 MinIO；AWS 可用 `-path-style=false` 走 virtual-host
- 只同步指定 `-prefix` 下的对象

### 常用参数

| 参数 | 说明 |
| --- | --- |
| `-dest` | 本地落盘目录（= 服务端 `ECY_DATA_DIR`） |
| `-workers` | 并发下载数（默认 4；网盘限流时调小） |
| `-max-depth` | 远端最大层级，需与服务端 `ECY_MAX_DEPTH` 一致 |
| `-ext` | 只同步这些后缀（默认图片+视频；`-ext '*'` 全同步） |
| `-prune` | 删除本地多余文件（远端已删） |
| `-dry-run` | 只打印计划不落盘，先看看会拉多少 |
| `-interval` | 常驻同步间隔（0 = 只跑一次） |

## 增量语义

| 情况 | 动作 |
| --- | --- |
| 本地缺失 | 下载 |
| 大小不同 | 重下 |
| 远端 mtime 更新（>1s） | 重下 |
| 完全一致 | 跳过（不产生 IO） |

下载保留远端 mtime（`Chtimes`），服务端的增量扫描正是按 mtime 判断，所以同步后只会重建真正变化的分类。

## 与「直连远端」的取舍

| | 镜像（本方案） | 直连远程索引 |
| --- | --- | --- |
| 改动面 | 新增独立工具，服务端零改动 | 索引/预览/缓存/内存全要重写 |
| 出图延迟 | 本地 sendfile，最快 | 受远端 RTT 影响 |
| 磁盘占用 | 需要容纳全量图片 | 只需缓存 |
| 断网可用性 | 完全可用 | 不可用 |
| 适合 | 图片库（总量可控、读取远多于变更） | 海量、极少访问的归档 |

图片库这种「写少读多、要缩略图和图库」的负载，镜像明显更合适。

## 测试

`internal/remote` 覆盖：

- `TestMirrorWebDAV`：假 WebDAV（PROPFIND + GET）→ 首次全下、二次零下载、远端新增只下 1 个、prune 删除本地多余
- `TestMirrorS3`：假 S3（ListObjectsV2 + GetObject）→ 目录聚合、下载、增量

## 路线图

- [x] WebDAV 镜像（OpenList / AList / Nextcloud / 群晖）
- [x] S3 镜像（AWS / MinIO / S3 网关）
- [ ] 断点续传（大视频文件）
- [ ] 同步前预检配额（避免把小磁盘撑满）
- [ ] 本地 LRU：镜像只保留「最近访问 + 最新」的图片，其余按需回源