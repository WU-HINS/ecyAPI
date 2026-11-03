# 远端后端（WebDAV / S3 / OpenList）

> **20 万张图的场景请直接看「规模：20 万张」一节**——镜像模式适合小库或子集预热，不适合全量。

## 两种模式

| 模式 | 本地磁盘 | 适用 |
| --- | --- | --- |
| **镜像**（已实现） | 全量图片 | 小库（≤ 1~2 万张）、或只镜像某个子集 |
| **远程直连**（规划中） | 只缓存缩略图 | 海量库（10 万+），原图不落盘 |

## 规模：20 万张

### 为什么镜像不行

```
20 万张 × 平均 2 MB ≈ 400 GB 本地磁盘
再加缩略图缓存 ~6 GB（200k × 30 KB）
```

为了「把远端当后端」却先要一块 400 GB 盘，这等于没上云。

### 远程直连该怎么做

```
远端 LIST（只取元数据） ──▶ 内存索引（20 万条约 30~60 MB）
                                  │
        ┌─────────────────────────┼──────────────────────────┐
        ▼                         ▼                          ▼
  /json 随机       /pic/... → 302 到远端直链         ?w=480 缩略图
  /img 图库          （S3 预签名 / OpenList 直链）    按需拉取 + 本地 LRU
```

- **元数据索引**：20 万条 → 路径 + 大小 + mtime，约 30~60 MB，512MB 容器放得下
- **原图**：`/pic/...` 直接 302 到 S3 预签名 URL（已实现 `SignURL`）或 OpenList 直链，
  源站零出流；CDN 还能吃 7 天缓存
- **缩略图**：必须本地生成，按需拉取原图 → 压缩 → 落 LRU（建议上限 2~5 GB）
- **图库页**：照旧由索引 + 缩略图拼，不依赖原图

### 已经就绪的积木

| 能力 | 状态 |
| --- | --- |
| WebDAV 客户端（PROPFIND/GET，OpenList/AList/Nextcloud/群晖） | ✅ |
| S3 客户端（SigV4：ListObjectsV2 / GetObject） | ✅ |
| S3 预签名 URL（源站直接出流） | ✅ |
| 增量镜像（小库 / 子集预热） | ✅ |
| 远程索引（store 层接远端 listing） | ⬜ 待做 |
| `/pic` 按来源分流（302 / 代理） | ⬜ 待做 |
| 缩略图 LRU + 按需拉取 | ⬜ 待做 |

## 镜像模式（已实现，适合小库/子集）

```bash
ecyapi-mirror -url https://openlist/dav -user admin -pass '密码' \
  -dest /srv/ecyapi/data/categories -workers 8 -max-depth 3 -prune
```

服务端 `ECY_DATA_DIR` 指向镜像目录即可；索引/缩略图/图库/内存控制全部复用。

S3：

```bash
ecyapi-mirror -src s3 -url https://minio:9000 -bucket pics -prefix library \
  -user AK -pass SK -dest /srv/ecyapi/data/categories
```

### 增量语义

| 情况 | 动作 |
| --- | --- |
| 本地缺失 / 大小不同 / 远端 mtime 更新 | 下载（临时文件 + rename） |
| 完全一致 | 跳过 |

下载保留远端 mtime，服务端增量扫描只会重建真正变化的分类。

## 测试

- `TestMirrorWebDAV`：假 WebDAV → 首次全下、二次零下载、新增只下 1 个、prune 删除
- `TestMirrorS3`：假 S3（ListObjectsV2 + GetObject）→ 目录聚合、下载、增量

## 路线图

- [x] WebDAV / S3 客户端与增量镜像
- [x] S3 预签名（原图 302 直出）
- [ ] **远程索引**：store 增加 remote 模式，从 listing 建索引（20 万条目标）
- [ ] **来源分流**：`/pic` 按「本地 / 远端直链 / 代理」自动选择
- [ ] **缩略图 LRU**：按需拉取 + 容量上限 + 冷热淘汰
- [ ] OpenList 直链支持（`/d/...?sign=`，公共存储免代理）