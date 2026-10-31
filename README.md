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

## 前端静态资源（/@index/*）

首页的看板娘（网页宠物）已经完全自托管，不再从第三方域名拉取：

| 资源 | 位置 | 体积 |
| --- | --- | --- |
| 脚本 `dsh-pet.standalone.js` | 内置（编译进二进制） | 46 KB |
| 配置 `assets/config.jsonc`、光标、字体 | 内置 | ~4 MB |
| 动画 `assets/webm/*.webm`（99 个） | 磁盘 `ECY_STATIC_DIR`（默认 `data/static`） | ~50 MB |

服务端按 **磁盘优先、内置兜底** 提供 `/@index/*`，并且页面引用的是**带版本前缀**的地址：

```
/@index/v2b563ceb/dsh-pet.standalone.js     → Cache-Control: public, max-age=604800, immutable
/@index/dsh-pet.standalone.js               → Cache-Control: public, max-age=604800
```

版本号取「内置资源内容 + 磁盘素材名/大小/mtime」的摘要，**素材或程序一变，前缀就变**，
所以 CDN 可以放心吃满 7 天缓存（默认值，可用 `ECY_STATIC_MAX_AGE` 调）。不带前缀的地址照旧可用，
只是没有 `immutable`。

- 内置资源开箱即用（脚本、配置、光标、字体）；
- 动画体积大，不随仓库分发，首次部署执行一次：

  ```bash
  make assets                 # 等价于 go run ./tools/fetchassets -dir data/static
  ecyapi -fetch-assets        # 服务端内置：下载到 ECY_STATIC_DIR 后退出（Docker 里最方便）
  ```

  已存在的文件会跳过，可重复执行；下载失败（比如分组标题被误当动画名）会跳过并计数。

想换目录就设 `ECY_STATIC_DIR`，把下载结果和这个变量指到同一处即可。

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
- **首次扫描为慢盘优化**：文件头探测并发进行（最多 4 个 goroutine，机械盘上让内核合并寻道），
  且**父目录聚合随子目录逐个完成即时可用**——扫到 `pixiv/c1` 时 `/pixiv` 已经能出图，不必等整棵子树扫完
- **启动优先读磁盘快照**：索引每 `ECY_SNAPSHOT_TTL`（默认 5 分钟）重写到 `data/index.snapshot.gz`；
  重启时若快照不超过 `ECY_SNAPSHOT_MAX_AGE`（默认 30 天），直接读回（5 万张库实测 **4.2s → 0.16s**），**完全不扫盘**，
  随后 3 秒一次的增量刷新会把停机期间的增删补齐
- **启动不等全量扫描**：没有可用快照时才扫描，且放后台，超过 `ECY_STARTUP_WAIT`（默认 15 秒）就先监听端口；
  首次扫描会**分批发布**已扫完的分类，服务启动时就能看到部分图库，剩余分类随后出现。
  `/healthz` 在扫描期间返回 `ok scanning`，`/readyz` 返回 503（K8s readiness 用它），扫描完成后转 200。
  扫描优先级高于后台预热：扫描期间预热完全让路
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
| `GET /@img/<name>` | **虚拟分组**：把若干分类/父目录组合成一个分组（单级），对外与普通分组一致——302 跳一张随机图，`?json` / `?json=N` / `?orientation=` / `?w=` 语义相同 |
| `GET /@img` | **全分类随机**：先从所有已注册分类里随机挑一个，再在其中随机取图；横竖按设备自动（桌面横图 / 移动竖图），可用 `?orientation=` 覆盖，`?json` 返回纯文本，`?w=` 透传预览宽度 |
| `GET /{分类路径}?json` | 返回纯文本随机链接，默认 1 条；`?json&quantity=5` 返回 5 条 |
| `GET /{分类路径}?json=86` | 按序号（文件名字典序，从 1 起）返回第 86 张图片链接 |

**父目录聚合**（`ECY_PARENT_AGG`，默认 `with_files`）：`pixiv/` 下既有图片又有 `KuroiDa/` 子目录时，
`/pixiv` = `pixiv` 自己的图 **+** `pixiv/KuroiDa` 的图（按数量加权随机）；`/pixiv/KuroiDa` 仍然独立可用。
设为 `dirs_only` 则回到旧行为：`/pixiv` 只出自己的图，只有「纯目录、没有图片」的父目录才做聚合。

**目录即接口**：把图片文件夹直接放进 `data/categories/`，`/{分类路径}` 立即可用，**不需要在后台建栏目**。
没有显式栏目时按「自动横竖」处理（桌面取横图、移动端取竖图），显式栏目优先。
目录名可以是 `pixiv/KuroiDa`、`2024-01`、`中文 目录` 这类常见命名。

栏目有三种形态，**默认是「自动横竖」**：

- **自动横竖（默认）**：只用一个分类，服务端扫描时读文件头判定每张图的方向，桌面端取横图、移动端取竖图。新建分类即自动获得该形态。
- **单端**：一个分类，不区分设备（后台建分类时勾选「关闭按设备自动分横竖」）。
- **双端**：后台分别绑定 PC 分类与移动端分类，按 UA 切换（升级前的经典用法，继续有效）。

`/@img` 会排除「图库隐藏」的分类（与 /img 的可见性一致）。

### 虚拟分组（/@img/<name>）

把若干已存在的分类组合成一个「类似父目录」的分组，定义在 `data/groups.json`：

```json
{"groups":[
  {"name":"wallpaper","label":"壁纸合集","desc":"桌面 + 4K","categories":["pc/hd","pc/4k"]},
  {"name":"all","label":"全部","categories":["pc","phone"]}
]}
```

- **单级**：`name` 只能用小写字母/数字与 `-_"，最长 32，不能带 `/`；`/@img/a/b` 一律 404
- 成员可以是普通分类，也可以是父目录（如 `pc`，会自动展开成它的聚合）
- `/@img/wallpaper` → **302 跳转**到组内一张随机图，和 `/pc` 这种普通分组一样
- `?json` 返回一条链接，`?json=5` / `?quantity=5` 返回 5 条；`?orientation=portrait`、`?w=1280` 同样支持
- 随机在**成员图片并集**上按数量加权（不是先随机分类再取图），成员大小差异大也不会被小分类稀释
- 该方向没有图时自动回退「不限方向」，不会因为分组里全是竖图就在桌面端 404
- 成员可以是普通分类，也可以是**父目录**（如 `pixiv` 自身没图、只有子目录）
- **首页与普通分类同款卡片**：显示完整链接，预览图直接指向 `/@img/<name>?w=480`，
  所以「点击图片刷新」= 重新随机一张（不是刷新同一张图）
- 后台「虚拟分组」页是**可视化列表**：每个分组一张卡片，勾选包含的分类（也可以直接手填路径，
  逗号/空格/换行分隔，不存在的会被忽略并在结果里提示）、改名字/标签、删除；新建分组也在同一页
- **首页会展示虚拟分组**：每个分组一张卡片（标签 + 描述 + 组内随机预览图），点击进入 `/@img/<name>`；
  组内没有可用图片的分组不会出现在首页

所有跳转接口都支持显式方向覆盖：`?orientation=landscape|portrait|auto`（也接受 `横`/`竖`/`h`/`v`）。该方向没有图时自动回退全部图片，不会 404。

`/@img` 是「全分类随机」入口：抽到的分类若恰好没有该方向的图（或记录都已失效），会换一个分类再试（最多 8 次），
所以不会因为某个分类只有竖图就在桌面端 404。分类名 `@img` 已保留，不能作为目录/栏目使用。

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
9. **登录与 SSO（OAuth2 / OIDC）**：
   - 填 **发现端点 / Issuer**，点「发现」自动填充授权端点、token 端点、JWKS、userinfo；也支持全部手填
   - **自动判定模式**：令牌响应里有 `id_token` 就按 OIDC 校验 JWT，没有则退回 OAuth2，用 `userinfo` 取身份；也可强制 `oidc`/`oauth`
   - **签名算法**：RSA（RS256/384/512、PS256/384/512）、EC（ES256/384/512，对应 P-256/P-384/P-521）、EdDSA（Ed25519）；
     JWKS 支持 `kid` 选键、缺 `kid`/`alg` 时逐个候选验签、只有 `x5c` 证书时从证书取公钥；明确拒绝 `alg=none`
     （`ES256K`/secp256k1 标准库不支持，暂未覆盖）
   - **必须先绑定**：SSO 身份未出现在「已绑定账号」里时一律拒绝（403），绑定入口是后台的「绑定当前 SSO 账号」
   - **仅使用 SSO**：开启后未登录访问后台直接跳转 IdP，密码登录被拒绝；未绑定任何账号时不允许开启（防锁死）
   - 接口：`GET /admin/sso/login[?bind=1]`、`GET /admin/sso/callback`、`POST /admin/sso/config`、
     `POST /admin/sso/discover`、`POST /admin/sso/unbind`
   - 安全：授权码 + PKCE(S256) + state（HttpOnly cookie 校验）、JWT 校验 iss/aud/exp/nonce；
     配置保存在 `data/sso.json`（0600），client_secret 明文，注意文件权限
8. **缓存与预热**：查看图库页/缩略图缓存占用；一键清理；按分类预热（可同时生成指定宽度的缩略图），带进度条与取消。
   - `POST /admin/cache/clear`、`POST /admin/cache/prewarm`（字段 `category`、`width`）、`GET /admin/cache/status`、`POST /admin/cache/cancel`
   - **预热是低优先级**：最近 1 秒有请求、内存压力 >0.3、或图库正在渲染时都会自动让路；
     缩略图最多只占一半像素预算，渲染只在空闲时进行；请求空闲后自动继续（不是放弃）

## 日志

启动与运行期只打有信息量的行，不刷屏：

```
首次扫描完成：30 个分类，读取 32 个目录，耗时 1ms 新增 pixiv/c1、pixiv/c10、pixiv/c11 等 30 个
首次扫描进行中：已加载 120 个分类（当前 pixiv/c120）        # 大库首扫每 5 秒一行
索引更新：重建 2 个目录（pixiv、pixiv/c99），耗时 717µs 新增 pixiv/c99（共 31 个分类）
已从磁盘快照恢复 12 个分类（保存于 2m0s 前），跳过启动扫描
管理操作：上传 3/3 个文件到分类 pc（客户端 1.2.3.4）
管理操作：删除分类 pc 下 2 张图片（a.png, b.png）
管理操作：创建分类 anime/2024（标签 "2024"，自动横竖）
管理操作：启动预热（12 个分类，缩略图宽度 480）
安全事件：绑定 SSO 身份 https://id.example.com / u-1（admin@example.com）
```

无变化时不打印（增量刷新每 3 秒一轮，静默）。

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
| `ECY_STARTUP_WAIT` | `15` | 首次扫描的等待上限（秒，0~300）。到点还没扫完就先监听端口、后台继续扫描；`0` = 等扫描完成再启动 |
| `ECY_STATIC_MAX_AGE` | `604800` | `/@index/*` 静态资源的 `Cache-Control: max-age`（秒，60~2592000，默认 **7 天**）。带版本前缀的 URL 会再加 `immutable` |
| `ECY_PARENT_AGG` | `with_files` | 父目录聚合模式：`with_files` = 父目录自己有图片时，`/父目录` = 自己的图 **+** 子目录的图；`dirs_only` = 只有自身没有图片的父目录才聚合子目录 |
| `ECY_VERIFY_INTERVAL` | `3600` | 强制完整校验间隔（秒，5~86400，默认 1 小时）。条目名没变时，最多隔这么久把该目录的文件 stat 一遍，用于发现「同名文件被原地替换」；库很大时可再调大以省 IO |
| `ECY_SNAPSHOT_TTL` | `300` | **运行期落盘周期**（秒，0~86400，默认 5 分钟）。每周期把内存索引重写一次（没变化也写，保证时间戳新鲜）；`0` = 关闭快照 |
| `ECY_SNAPSHOT_MAX_AGE` | `2592000` | **快照最大有效期**（秒，0~31536000，默认 30 天）。启动时超过该年龄的快照视为不可信、改为全量扫描；`0` = 不做过期判断 |
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

> **关于机械硬盘（HDD）**：本文与 `docs/benchmarks.md` 里的数字都是在 SSD/本地盘上测的。
> HDD 的随机寻道代价高一个数量级，**冷启动（首次全量扫描）会慢大约 10 倍**——5 万张从几秒变成几十秒甚至更久，
> 单目录新增一张的重建（逐文件 stat）也会明显变慢。缓解手段：
> 让磁盘快照保持有效（`ECY_SNAPSHOT_TTL` 内重启直接读回，不扫盘）、
> 把 `ECY_STARTUP_WAIT` 保持默认让服务先起来、以及尽量把图库放在 SSD 上。
> 运行期稳态（取图、302、缩略图命中）不受影响，因为它们只读内存索引与已缓存文件。

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
- **图库渲染缓存**：图库要「展示全部」，渲染结果按「索引代次 + 隐藏集合 + 抽样上限」落盘为 HTML（同时预生成 gzip 版本）。
  命中直接 sendfile，不占渲染名额：5 万张分类 100 并发从「87% 请求 503」变成 **0 失败**，p50 82ms；
  5 万张页面 37MB → gzip 后 770KB
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

### 发布产物（带素材的完整包）

Release 会产出三类文件：

| 文件 | 内容 |
| --- | --- |
| `ecyapi-<版本>-linux-<arch>.tar.gz` | **完整包**：`ecyapi` 二进制 + `data/static`（脚本/配置/字体/99 个动画）+ README，解压即用 |
| `ecyapi-<版本>-linux-<arch>` | 裸二进制（不含素材，适合自己挂卷） |
| `ecyapi-<版本>-static-assets.tar.gz` | 单独的素材包（给已有部署补素材） |

Docker 镜像里也内置了素材（`/app/data/static`），不用额外挂载。

本地打同样的包：

```bash
make dist VERSION=v1.0.3     # 产物在 dist/ecyapi-v1.0.3-linux-amd64.tar.gz
```

素材由 `tools/fetchassets` 从上游一次性拉取（约 50 MB，过程可重复执行、自动跳过已有文件）。


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

镜像里素材分两处，启动时自动同步：

| 路径 | 作用 |
| --- | --- |
| `/app/static` | 镜像层素材源（`ECY_STATIC_SYNC_FROM`） |
| `/app/data/static` | 运行目录（`ECY_STATIC_DIR`），位于数据卷内 |

容器启动时把「镜像里有、卷里没有/较旧」的文件补进 `/app/data/static`（只 stat + 增量拷贝，5 万文件级也就几十毫秒）。
所以即便 `/app/data` 被卷挂载，素材也会自动补齐；卷是只读时同步失败不影响启动——`/@index` 会直接回落到 `/app/static`。

补素材（容器内，无需 Go 工具链）：

```bash
docker exec <容器名> /app/ecyapi -fetch-assets
```

> 从 v1.0.3 升级的老部署：镜像把素材放在 `/app/data/static`，被数据卷遮住后取不到。
> 新版已改到 `/app/static`；老镜像可以用 docker cp 从镜像层里取出一次：
>
> ```bash
> cid=$(docker create ghcr.io/wu-hins/ecyapi:v1.0.3)
> docker cp "$cid:/app/data/static" ./static
> docker rm "$cid"
> # 然后把 ./static 挂到 /app/static（新版镜像不用挂）
> ```


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
