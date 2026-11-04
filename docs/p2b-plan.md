# P2b 实施计划：分类页 + aggUnit

> 现状：1M 稳态 177 MB（P2a 后），目标 ≤120 MB。差的 57 MB 全在「条目本体」。

## 一、当前结构（`internal/store/aggregate.go`）

```go
type aggChild struct {
    slices [][]fileEntry  // ← 直接引用 categorySet 的数组，导致数组无法释放
    cum    []int
    total  int
    weight float64
}
```

`pickOne()` → 选 child → `locate(k)` → `slices[i][j]` 取 `fileEntry` → `e.row()`。

问题：`aggChild.slices` 持有的是**切片头**，只要聚合存在，对应分类的条目数组就无法被 GC 回收。

## 二、目标结构

```go
// 聚合只存「哪个分类的第几段」，不持有条目数组
type aggUnit struct {
    cat   string // 分类名（用于页加载）
    base  uint32 // 在该分类 all/land/port 切面里的起始位置
    n     uint32 // 段内条目数（权重仍按整分类算，这里只用于偏移）
}

type aggChild struct {
    units  []aggUnit
    cum    []uint32   // 单位权重累积（uint32 足够，权重已归一）
    total  int
    weight float64
}
```

代价：每单位 4+8+16 ≈ 32B，**但单位是「分类」不是「条目」**——
2000 个分类 = 64KB，而原来是 100 万条 × 72B = 72MB。

## 三、需要改的函数（按顺序）

| 文件 | 函数 | 改动 |
| --- | --- | --- |
| aggregate.go | `buildAggBucket` | 不再切 `sets[cat].all`，改为记录 `aggUnit{cat, 0, n}` |
| aggregate.go | `aggChild.pick` | 选完 unit 后，用**页加载器**取该分类的切面，再按 offset 取行 |
| aggregate.go | `aggBucket.randomRows/rowAt/allRows` | 同上，签名增加 `load func(cat string, want Orientation) []fileEntry` |
| store.go | `RandomWith/ByIndexWith` | 调用聚合时传入 `f.pageFor(cat, want)` |
| store.go | 新增 `pageFor` | 命中 `f.cache[cat]` 直接返回；未命中则从 `index.bin` 物化（复用 compact 的 `compactCat`） |

## 四、分类页 LRU（第二步）

1. `categorySet` 增加 `lastUsed atomic.Int64`
2. 每次取用更新；后台每 30s 扫一遍，超过 `ECY_INDEX_PAGE_MB`（默认 200）就按最旧淘汰：
   `f.cache[cat] = nil`（保留 `f.counts[cat]` 供计数），`dirs[cat].set = nil`
3. 读取路径 `categorySet` 缺失时走 `pageFor` 从索引物化，物化后重新进入 LRU

⚠️ 注意：淘汰后 `Verify` 仍可用（它只 stat 文件，不依赖条目）。
⚠️ 快照保存需要条目——淘汰的分类在快照里只写 `count`，恢复时按需物化。

## 五、验收

```
tools/bench-1m.sh /tmp/bench1m 2000 500
  预期：RSS ≤120MB，/json ≥18k，/pixiv ≥4k（父聚合不退化）
  且：删掉一张图后 /pic 立即 404（Verify 语义保持）
```

## 六、风险

- 聚合是核心路径（`/@img`、父目录、`/json?N` 多行），改坏会直接影响首页
- 必须先用 `TestCompactReadParity` 那类**对照测试**（新旧实现对同一份数据抽取分布一致）
- 分类页淘汰与「扫描期写入」有并发风险：淘汰要在 `f.mu` 下与扫描互斥
## 关键发现：每条文件被存了两份（实测）

```
分类 c: all=30 land=30 port=0   → 30 个文件，存了 60 份 fileEntry
```

`categorySet` 有三个切面，且**每个切面存的是 fileEntry 值而不是索引**：

```go
type categorySet struct {
    all  []fileEntry   // 全部
    land []fileEntry   // 横版——与 all 重复存储
    port []fileEntry   // 竖版——与 all 重复存储
}
```

每张图要么在 land 要么在 port，所以**每条文件恒定被存 2 份**（72B × 2 = 144B）。

100 万张的账：

| 项 | 体积 |
| --- | --- |
| `all` 切面 | 1M × 72B = 72 MB |
| `land`/`port` 切面 | 1M × 72B = 72 MB |
| 字符串（name/link） | ~50 MB |
| 合计 | ≈ 194 MB（实测 177 MB，结构体大小估计略偏高） |

**把 land/port 改成 `[]uint32`（指向 all 的下标）即可省掉约 72 MB**：

```go
type categorySet struct {
    all      []fileEntry
    landIdx  []uint32
    portIdx  []uint32
}
```

177 MB − 72 MB ≈ **105 MB**，一步就能越过 120 MB 目标，
而且**不需要分类页 LRU**、不影响聚合、不影响 Verify、不影响快照。

改动面：`pick()` 的 8 个调用点（聚合 2 处、快照 1 处、热路径 2 处、计数 3 处）。
热路径可改成「按下标取单条」，冷路径（快照/聚合）再物化。