# ecyAPI — 栗次元随机图片 API（Go 版）

[![CI](https://github.com/WU-HINS/ecyAPI/actions/workflows/ci.yml/badge.svg)](https://github.com/WU-HINS/ecyAPI/actions/workflows/ci.yml)
[![Docker CI](https://github.com/WU-HINS/ecyAPI/actions/workflows/docker-ci.yml/badge.svg)](https://github.com/WU-HINS/ecyAPI/actions/workflows/docker-ci.yml)

无外部服务依赖、单二进制部署的随机图片服务。图片数据源为项目本地目录，无需 MySQL，开箱即用。

不内置任何分类与栏目，全部由管理员在后台按需创建；支持桌面/移动端自适应切换，内置网页图库与后台管理。

## 特性

- **零外部服务**：无 MySQL / Redis，仅 Go 标准库 + `golang.org/x/*` + libwebp（静态链接）
- **单二进制**：`go build` 一次打包即可部署，模板与静态资源内嵌，运行时无动态库依赖
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

图片文件直接存放在项目目录，以分类名为子文件夹；分类支持多级子目录：

```
data/categories/
├── {分类名}/           # 新建分类即在后台创建同名子目录
│   ├── a1.webp
│   └── a2.webp
└── {一级}/{二级}/      # 多级分类：后台填 anime/2024 即建两级目录
    ├── b1.webp
    └── b2.webp
```

- 数据根目录默认为 `data/categories`，可通过 `ECY_DATA_DIR` 修改
- **目录名规则很宽松**：允许中文、空格、`-`、`.`、`+`、`@`、`&` 等，数字开头也可以
  （`KuroiDa`、`2024-01`、`pixiv.1`、`中文 目录` 都行）；只拒绝：以 `.` 开头的隐藏目录、
  含 `/` `?` `#` `%` 或控制字符、首尾空格、以 `.` 结尾、单段 >128 字节
- 分类路径段数由 `ECY_MAX_DEPTH` 决定（默认 3，上限 8），以 `/` 分隔，总长 ≤512；首段不能与内置路由同名
  （`pic` / `json` / `admin` / `img` / `docs` / `healthz` / `favicon.ico`）
- **父目录聚合**：只承载子目录、自身没有图片的中间目录，访问 `/父目录` 会从**所有子分类**里随机出图
  （按图片数加权，`pixiv` → `pixiv/KuroiDa` + `pixiv/other/2024` …）；
  它不会出现在图库/后台的分类列表里，避免重复计数。子分类各自的 URL 也照常可用
- 分类路径中的 `/` 在 URL 里原样保留：`/pic/anime/2024/b1.webp`、`/json?anime/2024`
- 支持的文件类型：`jpg / jpeg / png / gif / webp / bmp / avif / svg / mp4 / webm`
- **符号链接**：指向目录的软链会被当作分类扫描（方便 `ln -s /mnt/图库 xxx`）；指向文件或失效、回环的软链会被跳过并记录原因
- **扫描不在请求路径上**：后台协程每 `ECY_SCAN_INTERVAL`（默认 3 秒）增量刷新，请求只读快照；
  上传/删除/新建分类走写路径立即生效。5 万张库以前的「每 3 秒一次 62ms 尾延迟」随之消失
- **死链自动跳过**：302 前确认目标文件还在（一次 stat），不在就从索引摘除并换一张，
  避免把用户跳到一个已删除的图片
- **被跳过的目录会在启动日志里列出原因**，后台「分类管理」页顶部也会显示同样的清单（名称不合法 / 软链失效 / 回环 / 路径过长）

双端自适应栏目的绑定关系保存在 `data/endpoints.json`，站点设置保存在 `data/settings.json`，改动日志保存在 `data/logs.json`，均由后台自动写入（均已在 `.gitignore` 中忽略）。

## 接口说明

### 随机跳转接口

| 接口 | 说明 |
| --- | --- |
| `GET /{分类路径}` | 302 跳转到该分类的一张随机图片（`/pc`、`/anime/2024` 均可） |
| `GET /{分类路径}?json` | 返回纯文本随机链接，默认 1 条；`?json&quantity=5` 返回 5 条 |
| `GET /{分类路径}?json=86` | 按序号（文件名字典序，从 1 起）返回第 86 张图片链接 |

**目录即接口**：把图片文件夹直接放进 `data/categories/`，`/{分类路径}` 立即可用，**不需要在后台建栏目**。
没有显式栏目时按「自动横竖」处理（桌面取横图、移动端取竖图），显式栏目优先。
目录名可以是 `pixiv/KuroiDa`、`2024-01`、`中文 目录` 这类常见命名。

栏目有三种形态，**默认是「自动横竖」**：

- **自动横竖（默认）**：只用一个分类，服务端扫描时读文件头判定每张图的方向，桌面端取横图、移动端取竖图。新建分类即自动获得该形态。
- **单端**：一个分类，不区分设备（后台建分类时勾选「关闭按设备自动分横竖」）。
- **双端**：后台分别绑定 PC 分类与移动端分类，按 UA 切换（升级前的经典用法，继续有效）。

所有跳转接口都支持显式方向覆盖：`?orientation=landscape|portrait|auto`（也接受 `横`/`竖`/`h`/`v`）。该方向没有图时自动回退全部图片，不会 404。

### JSON 接口

```bash
curl 'http://localhost:8080/json?{分类路径}'
# 多级分类（斜杠原样出现在 query key 中）
curl 'http://localhost:8080/json?anime/2024=3'
# 只要竖图 / 只要横图 / 跟随 UA
curl 'http://localhost:8080/json?anime/2024&orientation=portrait'
curl 'http://localhost:8080/json?pc=3&orientation=landscape'
curl 'http://localhost:8080/json?pc&orientation=auto'
```

方向参数别名：`orientation` / `orient` / `o`，取值 `landscape`(横) / `portrait`(竖) / `square` / `auto` / 留空(全部)。

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

**分类参数别名**（等价）：`table` / `type` / `category` / 直接以分类路径为键（含 `/`）。

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

`GET /pic/{分类路径}/{文件名}` 直接访问本地图片（如 `/pic/pc/a1.webp`、`/pic/anime/2024/b1.webp`），响应带 `Cache-Control: public, max-age=604800`（7 天），配合 CDN 可将源站压力降到接近零。

`?w=NNN` 追加在任意层级图片后即返回 WebP 压缩预览，缓存落在 `data/preview-cache/{分类路径}/`。

### 健康检查

`GET /healthz` 返回 `ok`。

## 横竖自动判定

分类里的每张图在**扫描时**读文件头判定一次方向，判定结果随缓存一起保存，读路径直接取用：

- 判定方式：`image.DecodeConfig`（只读头部，不解码像素），`宽 > 高` 为横版，`高 > 宽` 为竖版，等宽等高为正方形
- 覆盖格式：jpg / jpeg / png / gif / webp / bmp
- 不覆盖：mp4 / webm / svg / avif（判为 `unknown`，只出现在「不限方向」的结果里）
- 增量判定：按 `mtime + size` 复用上次结果，只有新增或改动过的文件才重新读头；整库重扫只做目录列举与 `stat`
- 缓存位置：内存（不落盘），进程重启后首次扫描重建
- 正方形与 unknown 不参与横竖筛选；某个方向没有图时，接口自动回退到全部，不会 404

已知限制：Go 标准库不解 EXIF，手机竖拍照片若以「横向像素 + Orientation=6」存储，会被判为横版。需要严格准确时得额外解析 EXIF 并交换宽高。

## 安全

- **登录限流**：同一 IP 5 分钟内失败 8 次即锁定 5 分钟，返回 `429` + `Retry-After`；成功登录清零。限流只信任连接来源 IP，不解析可伪造的 `X-Forwarded-For`
- **扩展名白名单**：`/pic` 只输出 jpg/jpeg/png/gif/webp/bmp/avif/svg/mp4/webm，分类目录里的其他文件一律 404
- **SVG 沙箱**：`.svg` 响应带 `Content-Security-Policy: sandbox`，禁止内联脚本；所有图片响应带 `X-Content-Type-Options: nosniff`
- **会话**：令牌 HttpOnly + SameSite=Lax，HTTPS（或反代声明 `X-Forwarded-Proto: https`）时附 `Secure`；每 10 分钟清理过期令牌
- **上传体量**：`ECY_MAX_UPLOAD_MB` 兜底，超限请求不会落盘

## 后台管理

访问 `/admin`，默认密码 `ecy-admin`（**生产环境务必通过 `ECY_ADMIN_PASSWORD` 修改**）。支持：

1. **图片管理**：选择分类 → 预览全部图片 → 勾选批量删除 / 多文件批量上传
2. **新建分类**：创建同名子目录，并自动上线首页卡片与 `/分类` 跳转接口；**默认按设备自动分横竖**（可勾选关闭）
3. **双端栏目绑定**：分别选择 PC 端分类与移动端分类，写入新栏目路径后生效
4. **站点设置**：配置对外 API 域名，接口输出链接统一使用该域名，留空则按请求自动推断
5. **首页文案**：编辑 slogan、分享语与 ICP 备案号文字及跳转链接（链接留空则不生成超链接）；文案支持少量 HTML 标签（`<br>` 换行、`<b>` 加粗、`<a href>` 链接等，详见后台提示），其余标签按纯文本原样显示
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
| `ECY_MAX_UPLOAD_MB` | `512` | 单次上传请求体上限（MB），超出直接拒绝 |
| `ECY_MAX_DEPTH` | `3` | 分类层级上限（1~8）。`3` 支持 `a/b/c`，调到 `5` 即支持 `a/b/c/d/e`；超过 8 按 8 处理 |
| `ECY_GALLERY_MAX` | `0` | 图库默认展示分类**全部**图片；设为 >0 时改为随机抽样 N 张（超大分类兜底，0~5000） |
| `ECY_GALLERY_CONCURRENCY` | `2` | 图库同时渲染的请求上限（1~16）。图库内存 = 并发数 × 单次渲染开销，靠它把峰值钉死 |
| `ECY_GALLERY_WAIT` | `5` | 图库排队等待秒数（1~60），超时返回 `503` + `Retry-After` |
| `ECY_PREVIEW_MP` | 自适应 | `?w=` 压缩的**总像素预算**（百万像素，8~512）。未设置时按 `GOMEMLIMIT/12` 推导（512MB 容器≈32MP，192MB≈15MP）；4K 图约 8MP，预算 15 表示同时压 1 张，其余请求回退原图并由后台补缓存 |
| `ECY_PREVIEW_CACHE_FILES` | `20000` | 缩略图缓存文件数上限（100~1000000）。太小会在浏览大图库时反复重压同一批图 |
| `ECY_MEMORY_LIMIT` | 自动探测 | **内存预算**（字节）。留空时取容器 cgroup 限制；运行时由调节器按预算动态设置 `GOMEMLIMIT`（55%~70%），让 RSS 稳定在预算的 ~75% |
| `ECY_MEM_SOFT_PCT` | `75` | 软目标百分比（40~90）。从这条线开始按比例收紧：并发许可、缩略图预算、排队策略随占用线性变严 |
| `ECY_MEM_HARD_PCT` | `90` | 红线百分比（60~99）。到达即紧急回收（GOMEMLIMIT 50%、GOGC 15、5 秒一次归还）并停止新的大分配 |
| `ECY_SCAN_INTERVAL` | `3` | 后台增量刷新间隔（秒，1~3600）。扫描已移出请求路径，文件增删在这个间隔内生效 |
| `ECY_VERIFY_LINKS` | 空（开启） | 设为 `off` 关闭 302 前的目标文件校验。开启时每次跳转多一次 stat（约 1~5µs），换来绝不把用户 302 到已删除文件 |
| `ECY_ACCESS_LOG` | 空（开启） | 设为 `off` 关闭访问日志；日志为异步写入，队列满即丢弃，不阻塞请求 |

## 栏目管理

程序**不内置任何分类与栏目**（无预置接口路径、无预置卡片），启动后从零开始，全部由管理员在后台创建：

- **新建分类**：后台创建分类即建立 `data/categories/{分类路径}/` 目录并自动上线首页卡片与 `/{分类路径}` 跳转接口；填 `anime/2024` 即建两级目录、两级路径
- **双端栏目绑定**：分别选择 PC 端分类与移动端分类，写入新栏目路径后生效，移动端 UA 自动分流
- **栏目排序**：拖拽调整首页卡片与栏目的展示顺序
- **持久化**：绑定关系保存在 `data/endpoints.json`（视频栏目 `kind: video` 首页以 `<video>` 预览）

> 已有旧部署升级注意：
> 1. 若 `data/endpoints.json` 为旧格式（仅保存自定义栏目），旧的内置栏目将不再自动出现，需在后台重新创建。
> 2. **单端图片栏目会自动升级为「自动横竖」**：桌面端只发横图、移动端只发竖图；某个方向没有图时自动回退全部，不会 404。
>    想保留升级前的「所有设备同一个池」，在 `endpoints.json` 对应条目里写 `"auto": false`，或删掉栏目后在后台勾选「关闭按设备自动分横竖」重建。

## 性能设计

面向万人并发场景，关键路径做了针对性优化：

- **节流重扫**：磁盘目录每 3 秒最多检查一次，读路径多数时候只比较一次时间戳，零阻塞；写操作立即失效缓存
- **按目录增量扫描**：每个目录只做一次 `ReadDir` 并与上次的条目名比对，未变化则整目录跳过（不 stat、不读文件头）；写路径强制完整校验。2000 张图的库，未变更时重扫 13~17ms → **2~3ms**
- **方向索引增量更新**：方向判定按 `mtime + size` 复用，只有新增/改动文件才读文件头；3000 张图冷启动约 180ms
- **异步访问日志**：请求路径只做一次 channel 投递，格式化与写盘在单独 goroutine，避免 `log` 全局锁成为热点
- **JSON 手写序列化**：不构造 `map[string]any`、不走反射，单请求分配 53 → 32 次
- **ETag + 304**：图片与缩略图带弱 ETag，浏览器重复访问走 304，省掉整包传输
- **静态文件零拷贝**：`/pic` 走 `ServeContent` + `SectionReader.WriteTo`，真实连接上落到 sendfile
- **缩略图单飞**：同一图同宽度的并发请求只压缩一次，其余等缓存落盘后直接命中
- **解压炸弹防护**：解码前用 `image.DecodeConfig` 预检，超过 4000 万像素直接拒绝
- **内存调节器（memguard）**：每秒读一次 RSS/堆（`/proc/self/statm` + `runtime/metrics`，不停世界），
  在软目标 75% 与红线 90% 之间**按压力比例收紧**：压力 `p=(占用-75%)/(90%-75%)`，
  并发许可 4→0、缩略图像素预算 ×(1-p)、图库在 p≥0.5 时不再排队；
  到红线触发紧急回收（GOMEMLIMIT 降到 50%、GOGC 15、每 5 秒归还一次），
  空闲时 30 秒无请求且堆 >64MB 才归还，且**每分钟最多一次**
- **内存硬上限（防 OOM）**：`GOMEMLIMIT` 自动取容器 cgroup 限制的 75%（不再默认 512MB 贴边）；
  图库保留「展示全部」，靠**并发闸门**（默认 2）把渲染内存钉死（5 万张 50 并发 RSS 388MB → 49MB，超限排队 5s 后 503）；
  缩略图按自适应像素预算限流，预算不足时**立刻回退原图 + 后台排队补缓存**，既不阻塞请求也不会永远填不满缓存
- **无锁随机**：使用 `math/rand/v2` 全局 ChaCha8 随机源，自动按 goroutine 分片，无锁竞争
- **部分洗牌**：只对前 `limit` 个元素做 Fisher-Yates 洗牌，复杂度 O(limit) 而非 O(n)
- **静态资源免日志**：`/pic/` 大流量请求跳过访问日志，避免标准库 log 互斥锁成为热点
- **超时与限流**：HTTP 服务配置读/写/空闲超时，防止慢客户端耗尽连接
- **CDN 友好**：图片响应带 7 天强缓存头
- **h2c 回源**：明文端口直接讲 HTTP/2（prior knowledge 与 Upgrade 均支持），CDN 可用 HTTP/2 回源
- **优雅关闭**：收到 SIGTERM/SIGINT 后停止接收新连接，最多等 15 秒让在途请求（含上传）收尾

## 构建

需要 Go 1.27+ 与 C 工具链（`?w=` 缩略图依赖 libwebp，通过 CGO 链接）。三种构建方式：

```bash
# 1) 默认：linux/amd64 直接链接仓库内置的 internal/webp/lib/linux_amd64/libwebp.a
CGO_ENABLED=1 go build -o ecyapi ./cmd/server

# 2) 系统 libwebp（linux/arm64、Docker、发行版打包场景）
#    2a. libwebp.a 自包含 sharpyuv（Debian 12 / Ubuntu 22.04）
sudo apt-get install -y libwebp-dev
CGO_ENABLED=1 go build -tags webp_system \
  -ldflags "-s -w -extldflags '-static'" -o ecyapi ./cmd/server

#    2b. libwebp ≥ 1.3（Ubuntu 24.04 / Debian 13，sharpyuv 被拆成独立库）
sudo apt-get install -y libwebp-dev libsharpyuv-dev
CGO_ENABLED=1 go build -tags "webp_system webp_sharpyuv" \
  -ldflags "-s -w -extldflags '-static'" -o ecyapi ./cmd/server

# 3) 不需要缩略图：纯 Go 构建，?w= 自动回退原图
CGO_ENABLED=0 go build -o ecyapi ./cmd/server
```

`-tags webp_system` 是跨架构构建的开关：仓库只内置了 linux/amd64 的静态库，
其他架构（含 linux/arm64）必须装 libwebp 后加该标签。产物可用 `file ecyapi` 确认
显示 `statically linked`。

### 测试

```bash
CGO_ENABLED=1 go test -tags webp_system -race ./...   # 全量测试（含竞态）
go test -run XXX -bench . ./internal/...              # 基准测试
```

## Docker

```bash
# 多架构镜像：ghcr.io/wu-hins/ecyapi（linux/amd64 + linux/arm64 原生构建）
docker run -d --name ecyapi -p 8080:8080 \
  -v /srv/ecy/data:/app/data \
  ghcr.io/wu-hins/ecyapi:latest
```

标签策略：

| 标签 | 何时更新 | 说明 |
| --- | --- | --- |
| `ci-<full sha>` / `ci-<short sha>` | 每次 push 到 main | 对应提交的 CI 构建，可用于复现问题 |
| `ci` | 每次 push 到 main | 指向最新 CI 构建（滚动标签） |
| `vX.Y.Z` | 手动发版（Release 工作流） | 正式版，对应同版本号的二进制 |
| `latest` | 手动发版且勾选 `push_latest` | **CI 工作流永不更新 latest** |

镜像内以 root 运行，直接挂载宿主目录无需处理属主；`HEALTHCHECK` 探活 `/healthz`。

### 实例编排

`deploy/` 下给了四种开箱即用的编排，细节见 [deploy/README.md](deploy/README.md)：

| 文件 | 形态 |
| --- | --- |
| `deploy/docker-compose.yml` + `.env.example` | 单实例（默认推荐），数据全在 `./data` |
| `deploy/compose.ha.yml` + `Caddyfile` | 1 写入实例 + 2 只读副本 + Caddy，`/admin` 固定打写入实例 |
| `deploy/k8s/ecyapi.yaml` | K8s：Secret + PVC + Deployment（Recreate，单副本）+ Service + Ingress |
| `deploy/systemd/ecyapi.service` | 裸机二进制 + systemd 加固单元 |

```bash
cd deploy && cp .env.example .env && vi .env   # 改 ECY_ADMIN_PASSWORD
docker compose up -d
./smoke.sh http://127.0.0.1:8080 <分类名>      # 健康检查 / JSON / 原图 / 缩略图
```

## CI 与发版

| 工作流 | 触发 | 行为 |
| --- | --- | --- |
| `.github/workflows/ci.yml` | push / PR | gofmt、vet、race 测试、静态编译冒烟 |
| `.github/workflows/docker-ci.yml` | push main（可手动） | 在 amd64 与 arm64 **原生 runner** 上各构建一次，合并为多架构清单，只推 `ci-*` 标签，**不碰 latest** |
| `.github/workflows/release.yml` | **手动触发** | 校验版本号 → 构建两个架构的静态二进制 → 创建 GitHub Release（含 SHA256SUMS）→ 构建并推送正式版镜像 `vX.Y.Z`，可选同步 `latest` |

发版操作（二进制与正式版镜像由同一次运行产出）：

```bash
gh workflow run release.yml -f version=v1.2.3
# 预发布：-f version=v1.3.0-rc.1 -f prerelease=true
# 不动 latest：-f push_latest=false
```

两个架构都在各自的原生 runner 上编译（amd64 → `ubuntu-latest`，arm64 → `ubuntu-24.04-arm`），
不使用 QEMU 模拟，因此产物与本机编译一致，构建耗时也可控。

## 目录结构

```
.
├── cmd/server/            # 服务入口（含优雅关闭）
├── internal/
│   ├── config/            # 环境变量配置加载
│   ├── server/            # HTTP 路由、处理器、模板与静态资源
│   ├── store/             # 本地文件数据源（随机取图 / 分类 / 上传 / 删除 / 方向索引）
│   └── webp/              # libwebp 绑定（CGO；默认内置 amd64 静态库）
├── deploy/                # 实例编排：compose / HA / k8s / systemd / 冒烟脚本
├── tools/orient/          # 离线横竖分拣工具（可选）
├── .github/workflows/     # CI / Docker CI / Release
└── data/                  # 运行数据（图片、绑定、设置，默认不入库）
```

## 上游与来源

本仓库是 [pupu/ecyAPI](https://cnb.cool/pupu/ecyAPI)（「栗次元」随机图片 API）的衍生版本，上游 Git 历史完整保留。

| 项 | 值 |
| --- | --- |
| 上游仓库 | `https://cnb.cool/pupu/ecyAPI`（远端名 `cnb`） |
| 本仓库 | `https://github.com/WU-HINS/ecyAPI`（远端名 `origin`） |
| 分叉基线 | `3de2f7a`（上游 main，2026-10-22，合并请求 #72） |
| 上游历史 | 99 个提交，全部保留 |
| 本仓库增量 | 在上游基线之上叠加的提交，见 `git log 3de2f7a..main` |

相对上游的主要改动：多级分类（`a/b`）、扫描期方向判定与「按设备自动分横竖」默认开启、目录级增量扫描、异步访问日志与手写 JSON 序列化、ETag/304、缩略图单飞、登录限流与 SVG 沙箱等安全加固、CGO 三种链接方式（含 linux/arm64）、GitHub CI/Docker CI/Release 三条流水线。Go 模块名、目录结构与接口契约保持兼容，已有部署可直接替换二进制升级。

### 同步上游

```bash
git fetch cnb
git log --oneline main..cnb/main     # 上游是否有新提交
git merge cnb/main                   # 冲突大概率集中在本次改动涉及的 server/store
```

上游的 cnb.cool 流水线配置（`.cnb.yml`）原样保留，不影响 GitHub Actions。

### 许可证状态

上游 README 声明「License 无（如需使用请遵循仓库约定）」，即**未附开源许可证**；本仓库公开托管不等于授权范围扩大。
如需商用或再分发，请先与上游作者确认，或由本仓库维护者补一份明确的 LICENSE（届时同步更新本节）。

## License

无（如需使用请遵循仓库约定）。
