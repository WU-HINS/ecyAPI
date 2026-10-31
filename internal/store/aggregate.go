package store

import (
	"math"
	"math/rand/v2"
	"sort"
)

// 父目录聚合：把子分类的图片集合按数量加权聚合，让「父目录」也能随机出图
// （/pixiv、/json?pixiv）。两种模式（ECY_PARENT_AGG）：
//   - with_files（默认）：父目录自己有图片时，聚合 = 自己的图片 + 子目录；
//   - dirs_only：父目录自己有图片时就是普通分类，不聚合子目录。
//
// 内存开销：每个父目录只保存对子分类切片（slice header）的引用，不复制图片条目。

// aggChild 聚合里的一个抽样单位（展开到 ECY_MAX_DEPTH 后的一个目录）。
//
// 权重 = sqrt(图片数)：整体仍按图片数分权（大目录更容易被选中），
// 但用平方根压缩差距——100 张 vs 1 张 的权重是 10:1 而不是 100:1，
// 保证小目录不会被大目录挤到几乎随机不到。
type aggChild struct {
	slices [][]fileEntry
	cum    []int // 单位内部按图片数定位
	total  int
	weight float64
}

// aggFloorWeight 每个抽样单位的「底分」：保证再怎么少的目录也不是 0 分。
// 可由 ECY_AGG_FLOOR 调整（默认 1，相当于「再小的目录也按 1 张参与」）。
var aggFloorWeight = 1.0

// SetFloorWeight 设置单位底分（0 表示关闭，退化为纯 sqrt）。
func SetFloorWeight(f float64) {
	if f < 0 {
		f = 0
	}
	if f > 100 {
		f = 100
	}
	aggFloorWeight = f
}

// UnitWeight 对外暴露的权重函数：sqrt(图片数) + 底分。
// /@img 与虚拟分组的「分类加权」也用它，保证全站一个口径。
func UnitWeight(total int) float64 { return unitWeight(total) }

// unitWeight 目录权重：sqrt(图片数) + 底分。
//
// 用平滑函数而不是死板的比例：即使 1 万张 vs 1 张，小目录也保有一点权重；
// 底分让「极小但存在」的目录不会被彻底淹没，同时不改变大目录整体占优的格局。
func unitWeight(total int) float64 {
	if total <= 0 {
		return 0
	}
	return math.Sqrt(float64(total)) + aggFloorWeight
}

// pick 单位内部按图片数均匀取一条。
func (c *aggChild) pick() (Row, bool) {
	if c.total == 0 || len(c.slices) == 0 {
		return Row{}, false
	}
	k := rand.IntN(c.total)
	for i, n := range c.cum {
		if k < n {
			prev := 0
			if i > 0 {
				prev = c.cum[i-1]
			}
			return c.slices[i][k-prev].row(), true
		}
	}
	return Row{}, false
}

// aggBucket 一个方向（全部/横/竖）的聚合桶。
type aggBucket struct {
	children []aggChild // 抽样单位：展开到 ECY_MAX_DEPTH 的目录
	// 单位权重累计，用于按权重挑单位
	wcum []float64
	wsum float64
	// slices/cum/total 是展平视图，供按序号定位与整体列举（顺序稳定）
	slices [][]fileEntry
	cum    []int
	total  int
	want   Orientation
}

// aggKids 一个父目录的三个方向桶（附带直接子目录名，供更深层展开）。
type aggKids struct {
	all      aggBucket
	land     aggBucket
	port     aggBucket
	children []string
}

func (a *aggKids) bucket(want Orientation) *aggBucket {
	switch want {
	case OrientationLandscape:
		return &a.land
	case OrientationPortrait:
		return &a.port
	}
	return &a.all
}

// newAggKids 由子目录名构造聚合。
func newAggKids(children []string, sets map[string]*categorySet, aggs map[string]*aggKids, maxDepth int) *aggKids {
	return newAggKidsWithSelf(children, sets, aggs, nil, maxDepth)
}

// newAggKidsWithSelf 与 newAggKids 相同，但把父目录自身的图片也并进聚合
// （自身图片作为一个独立单位参与加权）。
func newAggKidsWithSelf(children []string, sets map[string]*categorySet, aggs map[string]*aggKids, self *categorySet, maxDepth int) *aggKids {
	if maxDepth < 1 {
		maxDepth = DefaultMaxDepth
	}
	agg := &aggKids{
		children: append([]string(nil), children...),
		all:      buildAggBucket(children, sets, aggs, OrientationAny, self, 1, maxDepth),
		land:     buildAggBucket(children, sets, aggs, OrientationLandscape, self, 1, maxDepth),
		port:     buildAggBucket(children, sets, aggs, OrientationPortrait, self, 1, maxDepth),
	}
	if agg.all.total == 0 {
		return nil
	}
	return agg
}

// buildAggBucket 构造一个方向的聚合桶。
//
// 展开深度由 depth 与 maxDepth（ECY_MAX_DEPTH）决定：
//   - 还没到 maxDepth 且子目录本身是聚合 → 继续展开它的子目录
//   - 到达 maxDepth 或子目录是普通分类 → 作为一个抽样单位
//
// 单位之间按 sqrt(图片数) 加权，单位内部按图片数均匀。
func buildAggBucket(children []string, sets map[string]*categorySet, aggs map[string]*aggKids, want Orientation, self *categorySet, depth, maxDepth int) aggBucket {
	b := aggBucket{want: want}

	addUnit := func(slices [][]fileEntry, cum []int, total int) {
		if total == 0 || len(slices) == 0 {
			return
		}
		w := unitWeight(total)
		if w <= 0 {
			return
		}
		b.children = append(b.children, aggChild{slices: slices, cum: cum, total: total, weight: w})
		b.wsum += w
		b.wcum = append(b.wcum, b.wsum)
		for i, entries := range slices {
			n := cum[i]
			if i > 0 {
				n -= cum[i-1]
			}
			if n == 0 {
				continue
			}
			b.slices = append(b.slices, entries)
			b.total += n
			b.cum = append(b.cum, b.total)
		}
	}

	// 父目录自身图片：一个独立单位
	if self != nil {
		if entries := self.pick(want); len(entries) > 0 {
			addUnit([][]fileEntry{entries}, []int{len(entries)}, len(entries))
		}
	}

	var walk func(names []string, d int)
	walk = func(names []string, d int) {
		for _, name := range names {
			if set, ok := sets[name]; ok {
				if entries := set.pick(want); len(entries) > 0 {
					addUnit([][]fileEntry{entries}, []int{len(entries)}, len(entries))
				}
				continue
			}
			a, ok := aggs[name]
			if !ok {
				continue
			}
			sub := a.bucket(want)
			if sub.total == 0 {
				continue
			}
			// 还能再展开，且它确实有子目录 → 继续往下（按 ECY_MAX_DEPTH 控制展开层数）
			if d < maxDepth && len(a.children) > 0 {
				walk(a.children, d+1)
				continue
			}
			addUnit(sub.slices, sub.cum, sub.total)
		}
	}
	walk(children, depth)
	return b
}

// pickOne 抽一条：先按 sqrt(图片数) 权重挑单位，再在单位内按图片数均匀取。
func (b *aggBucket) pickOne() (Row, bool) {
	if len(b.children) == 0 || b.wsum <= 0 {
		return Row{}, false
	}
	k := rand.Float64() * b.wsum
	idx := sort.SearchFloat64s(b.wcum, k)
	if idx >= len(b.children) {
		idx = len(b.children) - 1
	}
	return b.children[idx].pick()
}

// locate 把全局下标映射到 (子集合, 集合内下标)。
func (b *aggBucket) locate(k int) (int, int) {
	lo, hi := 0, len(b.cum)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if b.cum[mid] <= k {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	prev := 0
	if lo > 0 {
		prev = b.cum[lo-1]
	}
	return lo, k - prev
}

// randomRows 从聚合桶里随机取 limit 条（不放回）。
//
// 抽样口径是「逐层平权」：每一层都在子节点之间等概率，嵌套父目录递归下去，
// 直到落在叶子分类里再随机取图。这样
//   - 5 万张的大子目录不会把 100 张的小子目录淹没；
//   - 子目录多的分支也不会因为「叶子多」就压过子目录少的分支。
func (b *aggBucket) randomRows(limit int) []Row {
	if b.total == 0 || limit < 1 || len(b.children) == 0 {
		return nil
	}
	if limit > b.total {
		limit = b.total
	}
	rows := make([]Row, 0, limit)
	seen := make(map[string]struct{}, limit)
	maxTries := limit * 50
	if maxTries < 500 {
		maxTries = 500
	}
	for tries := 0; len(rows) < limit && tries < maxTries; tries++ {
		row, ok := b.pickOne()
		if !ok {
			continue
		}
		if _, dup := seen[row.Link]; dup {
			continue
		}
		seen[row.Link] = struct{}{}
		rows = append(rows, row)
	}
	return rows
}

// rowAt 取第 index 个（从 0 起）。
func (b *aggBucket) rowAt(index int) (Row, bool) {
	if index < 0 || index >= b.total {
		return Row{}, false
	}
	si, ei := b.locate(index)
	return b.slices[si][ei].row(), true
}

// allRows 展开全部（父目录很少被整体列举，调用频率低）。
func (b *aggBucket) allRows() []Row {
	rows := make([]Row, 0, b.total)
	for _, entries := range b.slices {
		for _, e := range entries {
			rows = append(rows, e.row())
		}
	}
	return rows
}
