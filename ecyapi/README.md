# ecyAPI — 栗次元随机图片 API（Go 版）

从原 PHP 版（lcytuAPI）参考重写的 Go 语言实现。零第三方依赖，单二进制部署。

## 功能

- `GET /` — API 发布主页（分类卡片预览、改动日志）
- `GET /docs` — JSON 参数文档页
- `GET /img` — 图库页（分类切换、懒加载、大图预览、右键复制链接）
- `GET /json?{分类}` — 动态 JSON 接口，`?pc=3` 返回多条
- `GET /{分类}` — 302 跳转到随机图片，如 `/pc`、`/ycy`（移动端自动切竖图）
- `GET /{分类}?json` — 返回纯文本链接；`?json=86` 按序号取图
- `GET /admin` — 后台管理（密码登录、分类表编辑、新建分类）

## 数据源

默认使用 `data/categories/*.txt` 文本文件（每行一条图片链接），开箱即用。
也支持 MySQL（表结构 `id INT AUTO_INCREMENT PRIMARY KEY, link VARCHAR(1000) NOT NULL`），
通过配置文件或环境变量开启：

```bash
ECY_DB_HOST=127.0.0.1 ECY_DB_USER=u ECY_DB_PASSWORD=p ECY_DB_NAME=db ./ecyapi
```

未配置数据库时自动降级为文件数据源。

## 配置

环境变量（均带默认值）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `ECY_ADDR` | `:8080` | 监听地址 |
| `ECY_DATA_DIR` | `data/categories` | 文本数据源目录 |
| `ECY_ADMIN_PASSWORD` | `ecy-admin` | 后台密码（务必修改） |
| `ECY_MAX_LINKS` | `50` | 单次请求最大返回条数 |
| `ECY_DB_HOST/PORT/USER/PASSWORD/NAME/CHARSET` | 空 | MySQL 连接信息 |

## 运行

```bash
cd ecyapi && go run ./cmd/server
# 打包
go build -o ecyapi ./cmd/server && ./ecyapi
```

## 分类

pc 横图 / mp 竖图 / moe 萌版横图 / moemp 萌版竖图 / moez 萌版自适应 /
ai AI 横图 / aimp AI 竖图 / ys 原神横图 / ysmp 原神竖图 / ysz 原神自适应 /
fj 风景横图 / bd 白底横图 / tx 头像方图 / lai 七濑胡桃 / xhl 小狐狸 /
acg 动图视频 / ycy 二次元自适应
