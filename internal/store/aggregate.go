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

// aggDepthDecay 每深入一层的权重衰减：越深的目录拆分出的单位越多，
// 用衰减抵消「拆得越细权重越高」的倾向（默认 0.7，即每层打七折）。
var aggDepthDecay = 0.7

// SetDepthDecay 设置层级衰减（1 表示不衰减，<1 更深层更轻）。
func SetDepthDecay(d float64) {
	if d <= 0 || d > 1 {
		d = 1
	}
	aggDepthDecay = d
}

// depthFactor 第 level 层单位的衰减系数（level 从 1 开始）。
func depthFactor(level int, decay float64) float64 {
	if level <= 1 || decay >= 1 {
		return 1
	}
	f := 1.0
	for i := 1; i < level; i++ {
		f *= decay
	}
	return f
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
// 公平性设计（消除「拆成多个子目录就能多拿权重」的优势）：
//  1. 先按 ECY_MAX_DEPTH 把每个二级目录展开成若干「单位」（到深度上限或叶子为止）
//  2. 每个二级目录作为一个整体拿权重：sqrt(该分支图片总数) + 底分
//  3. 该权重再按 sqrt(各单位图片数) 的比例分给内部单位
//
// 这样 pixiv/2024{a..e 各 1 张}（5 张，拆成 5 个目录）与 pixiv/2025/only（5 张，1 个目录）
// 总量相同 → 权重相同（各约 50%），不会因为目录拆得细就更吃香；
// 而 100 张的分支仍然明显重于 1 张的分支。
func buildAggBucket(children []string, sets map[string]*categorySet, aggs map[string]*aggKids, want Orientation, self *categorySet, depth, maxDepth int) aggBucket {
	b := aggBucket{want: want}

	type unit struct {
		slices [][]fileEntry
		cum    []int
		total  int
		level  int // 相对分支根的层级（0 = 分支根自己）
	}
	type branch struct {
		units []unit
		total int
	}

	addBranch := func(br branch) {
		if br.total == 0 || len(br.units) == 0 {
			return
		}
		bw := unitWeight(br.total)
		if bw <= 0 {
			return
		}
		// 分支内按 sqrt(单位图片数) 分配，并按层级做衰减：
		// 越深的单位权重越低（decay^(level)），抑制「把目录拆深一层来多拿权重」的动机。
		sumSqrt := 0.0
		weights := make([]float64, len(br.units))
		for i, u := range br.units {
			w := math.Sqrt(float64(u.total))
			if aggDepthDecay > 0 && aggDepthDecay < 1 && u.level > 0 {
				w *= math.Pow(aggDepthDecay, float64(u.level))
			}
			weights[i] = w
			sumSqrt += w
		}
		for i, u := range br.units {
			w := bw
			if sumSqrt > 0 {
				w = bw * weights[i] / sumSqrt
			}
			if w <= 0 {
				continue
			}
			b.children = append(b.children, aggChild{slices: u.slices, cum: u.cum, total: u.total, weight: w})
			b.wsum += w
			b.wcum = append(b.wcum, b.wsum)
			for i, entries := range u.slices {
				n := u.cum[i]
				if i > 0 {
					n -= u.cum[i-1]
				}
				if n == 0 {
					continue
				}
				b.slices = append(b.slices, entries)
				b.total += n
				b.cum = append(b.cum, b.total)
			}
		}
	}

	// 父目录自身图片：单独一个分支
	if self != nil {
		if entries := self.pick(want); len(entries) > 0 {
			addBranch(branch{units: []unit{{slices: [][]fileEntry{entries}, cum: []int{len(entries)}, total: len(entries)}}, total: len(entries)})
		}
	}

	// collect 把一个目录（可能自身是聚合）展开成单位列表
	var collect func(name string, d int, lvl int, out *branch)
	collect = func(name string, d int, lvl int, out *branch) {
		if set, ok := sets[name]; ok {
			if entries := set.pick(want); len(entries) > 0 {
				out.units = append(out.units, unit{slices: [][]fileEntry{entries}, cum: []int{len(entries)}, total: len(entries), level: lvl})
				out.total += len(entries)
			}
			return
		}
		a, ok := aggs[name]
		if !ok {
			return
		}
		sub := a.bucket(want)
		if sub.total == 0 {
			return
		}
		if d < maxDepth && len(a.children) > 0 {
			for _, child := range a.children {
				collect(child, d+1, lvl+1, out)
			}
			return
		}
		// 到深度上限：整个子树作为一个单位
		out.units = append(out.units, unit{slices: sub.slices, cum: sub.cum, total: sub.total, level: lvl})
		out.total += sub.total
	}

	for _, child := range children {
		br := branch{}
		collect(child, depth, 0, &br)
		addBranch(br)
	}
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
