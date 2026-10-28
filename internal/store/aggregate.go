package store

import "math/rand/v2"

// 父目录聚合：data/categories/pixiv/ 下只有子目录、自身没有图片时，
// /pixiv 默认是没有随机结果的。这里把子分类的图片集合按数量加权聚合，
// 让「父目录」也能随机出图（/pixiv、/json?pixiv），但不出现在分类列表里，
// 避免图库/后台重复计数。
//
// 内存开销：每个父目录只保存对子分类切片（slice header）的引用，不复制图片条目。

// aggBucket 一个方向（全部/横/竖）的聚合桶。
type aggBucket struct {
	slices [][]fileEntry // 子集合在指定方向上的切片
	cum    []int         // 累计数量，用于二分定位
	total  int
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

// newAggKids 由子目录名构造聚合（子目录可能本身也是聚合目录，已展开为叶子）。
func newAggKids(children []string, sets map[string]*categorySet, aggs map[string]*aggKids) *aggKids {
	agg := &aggKids{
		all:  buildAggBucket(children, sets, aggs, OrientationAny),
		land: buildAggBucket(children, sets, aggs, OrientationLandscape),
		port: buildAggBucket(children, sets, aggs, OrientationPortrait),
	}
	if agg.all.total == 0 {
		return nil
	}
	return agg
}

func buildAggBucket(children []string, sets map[string]*categorySet, aggs map[string]*aggKids, want Orientation) aggBucket {
	var b aggBucket
	for _, child := range children {
		if set, ok := sets[child]; ok {
			entries := set.pick(want)
			if len(entries) == 0 {
				continue
			}
			b.slices = append(b.slices, entries)
			b.total += len(entries)
			b.cum = append(b.cum, b.total)
			continue
		}
		if a, ok := aggs[child]; ok {
			src := a.bucket(want)
			for i, entries := range src.slices {
				n := src.cum[i]
				if i > 0 {
					n -= src.cum[i-1]
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
func (b *aggBucket) randomRows(limit int) []Row {
	if b.total == 0 || limit < 1 {
		return nil
	}
	if limit > b.total {
		limit = b.total
	}
	rows := make([]Row, 0, limit)
	seen := make(map[int]struct{}, limit)
	for len(rows) < limit {
		k := rand.IntN(b.total)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		si, ei := b.locate(k)
		rows = append(rows, b.slices[si][ei].row())
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
