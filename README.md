# ecyAPI — 栗次元随机图片 API（Go 版）

零第三方依赖、单二进制部署的随机图片服务。图片数据源为项目本地目录，无需 MySQL，开箱即用。

不内置任何分类与栏目，全部由管理员在后台按需创建；支持桌面/移动端自适应切换，内置网页图库与后台管理。

## 特性

- **零依赖**：纯 Go 标准库实现，无 MySQL / Redis 等外部依赖
- **单二进制**：`go build` 一次打包即可部署，模板与静态资源内嵌
- **本地文件数据源**：图片以分类子目录存放，热更新（节流重扫，无需重启）
- **双端自适应**：桌面访问走横图分类，移动端 UA 自动切换竖图分类
- **高并发友好**：面向万人并发优化，详见「性能设计」
- **内置后台**：网页化管理图片、分类、双端栏目、站点设置与改动日志

## 快速开始

```bash
# 准备数据目录（分类名 = 子目录名）
mkdir -p data/categories/pc

# 运行
go run ./cmd/server

# 打包为单二进制
go build -o ecyapi ./cmd/server
./ecyapi
```

默认监听 `:8080`，访问：

- `http://localhost:8080/` — 发布主页
- `http://localhost:8080/docs` — JSON 参数文档
- `http://localhost:8080/img` — 图库
- `http://localhost:8080/admin` — 后台管理
- `http://localhost:8080/healthz` — 健康检查

## 数据源

图片文件直接存放在项目目录，以分类名为子文件夹：

```
data/categories/
├── {分类名}/    # 新建分类即在后台创建同名子目录
│   ├── a1.webp
│   └── a2.webp
└── ...
```

- 数据根目录默认为 `data/categories`，可通过 `ECY_DATA_DIR` 修改
- 分类名须符合 `[A-Za-z][A-Za-z0-9_]{0,63}`（首字符为字母，防路径穿越）
- 支持的文件类型：`jpg / jpeg / png / gif / webp / bmp / avif / svg / mp4 / webm`
- 读请求每 3 秒节流重扫一次目录，热更新无需重启；上传/删除/新建分类后立即生效

双端自适应栏目的绑定关系保存在 `data/endpoints.json`，站点设置保存在 `data/settings.json`，改动日志保存在 `data/logs.json`，均由后台自动写入（均已在 `.gitignore` 中忽略）。

## 接口说明

### 随机跳转接口

| 接口 | 说明 |
| --- | --- |
| `GET /{分类}` | 302 跳转到该分类的一张随机图片 |
| `GET /{分类}?json` | 返回纯文本随机链接，默认 1 条；`?json&quantity=5` 返回 5 条 |
| `GET /{分类}?json=86` | 按序号（文件名字典序，从 1 起）返回第 86 张图片链接 |

自适应栏目（在后台绑定 PC 分类与移动端分类）会检测请求 UA：移动端自动切到对应竖图分类，桌面端走横图分类。

### JSON 接口

```bash
curl 'http://localhost:8080/json?{分类}'
```

单条返回（以分类 `pc` 为例）：

```json
{
  "code": 200,
  "category": "pc",
  "count": 1,
  "data": {
    "id": "a1.webp",
    "link": "http://localhost:8080/pic/pc/a1.webp"
  }
}
```

多条：`/json?pc=3`（返回条数上限由 `ECY_MAX_LINKS` 控制）

```json
{
  "code": 200,
  "category": "pc",
  "count": 3,
  "data": [
    { "id": "a1.webp", "link": "http://localhost:8080/pic/pc/a1.webp" },
    { "id": "a2.webp", "link": "http://localhost:8080/pic/pc/a2.webp" },
    { "id": "a3.webp", "link": "http://localhost:8080/pic/pc/a3.webp" }
  ],
  "links": [
    "http://localhost:8080/pic/pc/a1.webp",
    "http://localhost:8080/pic/pc/a2.webp",
    "http://localhost:8080/pic/pc/a3.webp"
  ]
}
```

**分类参数别名**（等价）：`table` / `type` / `category` / 直接以分类名为键。

```bash
curl 'http://localhost:8080/json?table=pc'
curl 'http://localhost:8080/json?type=pc'
curl 'http://localhost:8080/json?category=pc'
```

**数量参数别名**（等价）：`num` / `count` / `limit` / `n` / 直接给分类参数赋数字值。

```bash
curl 'http://localhost:8080/json?pc=5'
curl 'http://localhost:8080/json?pc&num=5'
curl 'http://localhost:8080/json?category=pc&count=5'
```

- 未指定数量时默认返回 1 条
- 单次返回上限由 `ECY_MAX_LINKS` 控制（默认 50）
- 参数非法或分类不存在时返回「你好世界」页

### 图片访问

`GET /pic/{分类}/{文件名}` 直接访问本地图片，响应带 `Cache-Control: public, max-age=604800`（7 天），配合 CDN 可将源站压力降到接近零。

### 健康检查

`GET /healthz` 返回 `ok`。

## 后台管理

访问 `/admin`，默认密码 `ecy-admin`（**生产环境务必通过 `ECY_ADMIN_PASSWORD` 修改**）。支持：

1. **图片管理**：选择分类 → 预览全部图片 → 勾选批量删除 / 多文件批量上传
2. **新建分类**：创建同名子目录，并自动上线首页卡片与 `/分类` 跳转接口
3. **双端栏目绑定**：分别选择 PC 端分类与移动端分类，写入新栏目路径后生效
4. **站点设置**：配置对外 API 域名，接口输出链接统一使用该域名，留空则按请求自动推断
5. **首页文案**：编辑 slogan、分享语与 ICP 备案号
6. **改动日志**：在首页「更新日志」增删条目
7. **栏目排序**：拖拽调整首页卡片与栏目的展示顺序

## 配置

环境变量（均带默认值）：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `ECY_ADDR` | `:8080` | HTTP 监听地址 |
| `ECY_DATA_DIR` | `data/categories` | 图片数据根目录 |
| `ECY_ADMIN_PASSWORD` | `ecy-admin` | 后台登录密码，务必修改 |
| `ECY_MAX_LINKS` | `50` | 单次请求最大返回条数 |
| `ECY_BASE_URL` | 空 | 对外域名（如 `https://api.example.com`），JSON/纯文本输出链接使用；后台「站点设置」保存的值优先于该环境变量 |

## 栏目管理

程序**不内置任何分类与栏目**（无预置接口路径、无预置卡片），启动后从零开始，全部由管理员在后台创建：

- **新建分类**：后台创建分类即建立 `data/categories/{分类名}/` 目录并自动上线首页卡片与 `/{分类}` 跳转接口
- **双端栏目绑定**：分别选择 PC 端分类与移动端分类，写入新栏目路径后生效，移动端 UA 自动分流
- **栏目排序**：拖拽调整首页卡片与栏目的展示顺序
- **持久化**：绑定关系保存在 `data/endpoints.json`（视频栏目 `kind: video` 首页以 `<video>` 预览）

> 已有旧部署升级注意：若 `data/endpoints.json` 为旧格式（仅保存自定义栏目），旧的内置栏目将不再自动出现，需在后台重新创建。

## 性能设计

面向万人并发场景，关键路径做了针对性优化：

- **节流重扫**：磁盘目录每 3 秒最多扫描一次，读路径多数时候只比较一次时间戳，零阻塞；写操作立即失效缓存
- **无锁随机**：使用 `math/rand/v2` 全局 ChaCha8 随机源，自动按 goroutine 分片，无锁竞争
- **部分洗牌**：只对前 `limit` 个元素做 Fisher-Yates 洗牌，复杂度 O(limit) 而非 O(n)
- **静态资源免日志**：`/pic/` 大流量请求跳过访问日志，避免标准库 log 互斥锁成为热点
- **超时与限流**：HTTP 服务配置读/写/空闲超时，防止慢客户端耗尽连接
- **CDN 友好**：图片响应带 7 天强缓存头

## 开发

```bash
# 运行测试（含并发正确性与基准测试）
go test ./...

# 本地构建
go build ./...
go vet ./...
```

CI（`.cnb.yml`）：`main` 分支 push 时自动执行 `go build ./...` 与 `go vet ./...`。

## 目录结构

```
.
├── cmd/server/            # 服务入口
├── internal/
│   ├── config/            # 环境变量配置加载
│   ├── server/            # HTTP 路由、处理器、模板与静态资源
│   └── store/             # 本地文件数据源（随机取图 / 分类 / 上传 / 删除）
└── data/                  # 运行数据（图片、绑定、设置，默认不入库）
```

## License

无（如需使用请遵循仓库约定）。
