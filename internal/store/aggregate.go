package store

import "math/rand/v2"

// 父目录聚合：把子分类的图片集合按数量加权聚合，让「父目录」也能随机出图
// （/pixiv、/json?pixiv）。两种模式（ECY_PARENT_AGG）：
//   - with_files（默认）：父目录自己有图片时，聚合 = 自己的图片 + 子目录；
//   - dirs_only：父目录自己有图片时就是普通分类，不聚合子目录。
//
// 内存开销：每个父目录只保存对子分类切片（slice header）的引用，不复制图片条目。

// aggChild 聚合里的一个「平权单位」：要么是一个叶子分类的切片，要么是一个嵌套聚合并。
type aggChild struct {
	entries []fileEntry // 叶子分类在该方向上的图片
	agg     *aggKids    // 子目录本身也是聚合（父目录套父目录）
}

// aggBucket 一个方向（全部/横/竖）的聚合桶。
type aggBucket struct {
	// children 抽样用的层级结构：每个子节点等概率，嵌套聚合并递归下去。
	children []aggChild
	// slices/cum/total 是展平视图，供按序号定位与整体列举使用（顺序稳定）。
	slices [][]fileEntry
	cum    []int
	total  int
	// want 该桶对应的方向（递归抽样时保持一致）。
	want Orientation
}

// aggKids 一个父目录的三个方向桶。
type aggKids struct {
	all  aggBucket
	land aggBucket
	port aggBucket
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

// newAggKids 由子目录名构造聚合（子目录可能本身也是聚合目录）。
func newAggKids(children []string, sets map[string]*categorySet, aggs map[string]*aggKids) *aggKids {
	return newAggKidsWithSelf(children, sets, aggs, nil)
}

// newAggKidsWithSelf 与 newAggKids 相同，但把父目录自身的图片也并进聚合。
// 「有文件聚合子目录」模式用它：/pixiv 既能取到自己的图，也能取到子目录的图。
func newAggKidsWithSelf(children []string, sets map[string]*categorySet, aggs map[string]*aggKids, self *categorySet) *aggKids {
	agg := &aggKids{
		all:  buildAggBucket(children, sets, aggs, OrientationAny, self),
		land: buildAggBucket(children, sets, aggs, OrientationLandscape, self),
		port: buildAggBucket(children, sets, aggs, OrientationPortrait, self),
	}
	if agg.all.total == 0 {
		return nil
	}
	return agg
}

// buildAggBucket 构造一个方向的聚合桶：
//   - children 保留层级（子分类 / 子聚合），抽样时逐层平权；
//   - slices/cum 是展平结果，供按序号定位与列举（顺序 = 父自身图片在前，随后按子目录顺序）。
func buildAggBucket(children []string, sets map[string]*categorySet, aggs map[string]*aggKids, want Orientation, self *categorySet) aggBucket {
	b := aggBucket{want: want}
	if self != nil {
		if entries := self.pick(want); len(entries) > 0 {
			b.children = append(b.children, aggChild{entries: entries})
			b.slices = append(b.slices, entries)
			b.total += len(entries)
			b.cum = append(b.cum, b.total)
		}
	}
	for _, child := range children {
		if set, ok := sets[child]; ok {
			entries := set.pick(want)
			if len(entries) == 0 {
				continue
			}
			b.children = append(b.children, aggChild{entries: entries})
			b.slices = append(b.slices, entries)
			b.total += len(entries)
			b.cum = append(b.cum, b.total)
			continue
		}
		if a, ok := aggs[child]; ok {
			sub := a.bucket(want)
			if sub.total == 0 {
				continue
			}
			// 嵌套父目录：抽样时作为一个平权单位递归下去，展平时仍把叶子切片摊开
			b.children = append(b.children, aggChild{agg: a})
			for i, entries := range sub.slices {
				n := sub.cum[i]
				if i > 0 {
					n -= sub.cum[i-1]
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
	return b
}

// pickOne 递归抽一条：每一层都在子节点之间等概率，直到落在叶子分类里再随机取图。
func (b *aggBucket) pickOne() (Row, bool) {
	if len(b.children) == 0 || b.total == 0 {
		return Row{}, false
	}
	c := b.children[rand.IntN(len(b.children))]
	if c.agg != nil {
		return c.agg.bucket(b.want).pickOne()
	}
	if len(c.entries) == 0 {
		return Row{}, false
	}
	return c.entries[rand.IntN(len(c.entries))].row(), true
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
