# ecyAPI — 栗次元随机图片 API（Go 版）

零第三方依赖，单二进制部署。图片数据源为项目本地目录，不再依赖 MySQL。

## 功能

- `GET /` — API 发布主页（分类卡片预览）
- `GET /docs` — JSON 参数文档页
- `GET /img` — 图库页（分类切换、懒加载、大图预览）
- `GET /json?{分类}` — 动态 JSON 接口，`?pc=3` 返回多条
- `GET /{分类}` — 302 跳转到随机图片，如 `/pc`（移动端自动切竖图）
- `GET /{分类}?json` — 返回纯文本链接；`?json=86` 按序号取图
- `GET /pic/{分类}/{文件名}` — 本地图片访问
- `GET /admin` — 后台管理（密码登录、图片预览/上传/删除、新建分类、双端栏目绑定）

## 数据源

图片文件直接存放在项目目录中，以分类名为子文件夹：

```
data/categories/
├── pc/          # PC 横图
│   ├── a1.webp
│   └── a2.webp
├── mp/          # 移动竖图
│   └── ...
└── {分类名}/    # 新建分类即在后台创建同名子目录
```

支持的文件类型：`jpg / jpeg / png / gif / webp / bmp / avif / svg / mp4 / webm`。

双端自适应栏目绑定保存在 `data/endpoints.json`，由后台自动写入。

## 配置

环境变量（均带默认值）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `ECY_ADDR` | `:8080` | 监听地址 |
| `ECY_DATA_DIR` | `data/categories` | 图片数据目录 |
| `ECY_ADMIN_PASSWORD` | `ecy-admin` | 后台密码（务必修改） |
| `ECY_MAX_LINKS` | `50` | 单次请求最大返回条数 |
| `ECY_BASE_URL` | 空 | API 对外域名（如 `https://api.example.com`），JSON/纯文本输出链接使用；后台「站点设置」中亦可修改，后台保存的值优先 |

## 运行

```bash
go run ./cmd/server
# 打包
go build -o ecyapi ./cmd/server && ./ecyapi
```

## JSON 接口

```bash
curl 'http://localhost:8080/json?pc'
```

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

多条：`/json?pc=3`，返回 `data` 数组与 `links` 数组。

## 后台

访问 `/admin`：

1. 选择分类 → 预览该分类全部图片
2. 勾选图片可单张或批量删除
3. 选择文件可批量上传新增图片
4. 创建新分类（自动创建同名目录）
5. 站点设置：填写 API 对外域名，接口输出链接统一使用该域名，留空按请求自动推断
6. 新增双端自适应栏目：分别选择 PC 端分类与移动端分类，写入新栏目路径后生效，
   桌面访问走 PC 分类、移动端访问自动切到移动分类

## 分类

pc 横图 / mp 竖图 / moe 萌版横图 / moemp 萌版竖图 / moez 萌版自适应 /
ai AI 横图 / aimp AI 竖图 / ys 原神横图 / ysmp 原神竖图 / ysz 原神自适应 /
fj 风景横图 / bd 白底横图 / tx 头像方图 / lai 七濑胡桃 / xhl 小狐狸 /
acg 动图视频 / ycy 二次元自适应
