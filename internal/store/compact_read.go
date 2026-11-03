package store

// compact_read.go —— M2b-3：把读路径切到紧凑索引。
//
// 索引里每条记录带方向位（oraFlags），这里给每个分类维护「按方向过滤后的位置数组」，
// 随机/按序都在这份数组上做，与旧的 categorySet 语义一致：
//   - Any   = 全部
//   - Land  = 横 + 未知（未知视为可满足，保持与旧行为一致）
//   - Port  = 竖 + 未知
//
// 位置数组按分类做 LRU（默认保留 64 个分类），500 条/分类时约占 2KB×64 = 128KB。

import (
	"math/rand/v2"
	"sort"
	"sync"

	"ecyapi/internal/index"
)

type compactCat struct {
	cat  index.Category
	all  []uint32
	land []uint32
	port []uint32
}

type compactCache struct {
	mu    sync.Mutex
	items map[string]*compactCat
	order []string
	cap   int
	ix    *index.Index
}

func newCompactCache(ix *index.Index, capacity int) *compactCache {
	if capacity <= 0 {
		capacity = 64
	}
	return &compactCache{items: make(map[string]*compactCat), cap: capacity, ix: ix}
}

// get 取（必要时构建）某分类的过滤视图。
func (c *compactCache) get(name string) (*compactCat, bool) {
	c.mu.Lock()
	if cc, ok := c.items[name]; ok {
		c.mu.Unlock()
		return cc, true
	}
	c.mu.Unlock()

	cat, ok := c.ix.Find(name)
	if !ok {
		return nil, false
	}
	cc := &compactCat{cat: cat}
	cc.all = make([]uint32, 0, cat.Count)
	cc.land = make([]uint32, 0, cat.Count)
	cc.port = make([]uint32, 0, cat.Count)
	// 索引记录按 hash 排序（便于二分），但对外语义要求按名字排序
	//（旧 categorySet 是排序过的，ByIndex/分页依赖它）。这里重排一次并缓存。
	type named struct {
		pos  uint32
		name string
	}
	items := make([]named, 0, cat.Count)
	for i := 0; i < cat.Count; i++ {
		rec, ok := c.ix.At(cat, i)
		if !ok {
			continue
		}
		items = append(items, named{pos: uint32(i), name: rec.Name})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	for _, it := range items {
		rec, _ := c.ix.At(cat, int(it.pos))
		cc.all = append(cc.all, it.pos)
		switch rec.Flags & 0x3 {
		case index.OriLand:
			cc.land = append(cc.land, it.pos)
		case index.OriPort:
			cc.port = append(cc.port, it.pos)
		default:
			// 未知方向：两种都算（旧实现的 OrientationUnknown 也是这么处理的）
			cc.land = append(cc.land, it.pos)
			cc.port = append(cc.port, it.pos)
		}
	}

	c.mu.Lock()
	if len(c.items) >= c.cap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.items, oldest)
	}
	c.items[name] = cc
	c.order = append(c.order, name)
	c.mu.Unlock()
	return cc, true
}

// positions 按方向给出位置数组。
func (cc *compactCat) positions(want Orientation) []uint32 {
	switch want {
	case OrientationLandscape:
		return cc.land
	case OrientationPortrait:
		return cc.port
	}
	return cc.all
}

// row 由位置构造对外行。
func (cc *compactCat) row(ix *index.Index, pos uint32) (Row, bool) {
	rec, ok := ix.At(cc.cat, int(pos))
	if !ok {
		return Row{}, false
	}
	return Row{
		ID:    rec.Name,
		Link:  "/pic/" + cc.cat.Name + "/" + rec.Name,
		Width: 0, Height: 0,
		Orientation: Orientation(rec.Flags & 0x3).String(),
	}, true
}

// count 该方向的条目数。
func (cc *compactCat) count(want Orientation) int { return len(cc.positions(want)) }

// random 不放回随机取 limit 条。
func (cc *compactCat) random(ix *index.Index, want Orientation, limit int) []Row {
	pos := cc.positions(want)
	n := len(pos)
	if n == 0 || limit < 1 {
		return nil
	}
	if limit > n {
		limit = n
	}
	out := make([]Row, 0, limit)
	if limit*4 < n {
		// 稀疏取：随机抽样 + 去重
		seen := make(map[uint32]struct{}, limit)
		for tries := 0; len(out) < limit && tries < limit*32; tries++ {
			k := pos[rand.IntN(n)]
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			if row, ok := cc.row(ix, k); ok {
				out = append(out, row)
			}
		}
		return out
	}
	// 密集取：洗牌前 limit 个位置（Fisher-Yates，只动用到的那部分）
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	for i := 0; i < limit; i++ {
		j := i + rand.IntN(n-i)
		idx[i], idx[j] = idx[j], idx[i]
		if row, ok := cc.row(ix, pos[idx[i]]); ok {
			out = append(out, row)
		}
	}
	return out
}

// byIndex 第 i 条（从 1 起）。
func (cc *compactCat) byIndex(ix *index.Index, want Orientation, i int) (Row, bool) {
	pos := cc.positions(want)
	if i < 1 || i > len(pos) {
		return Row{}, false
	}
	return cc.row(ix, pos[i-1])
}

// page 分页列举。
func (cc *compactCat) page(ix *index.Index, want Orientation, offset, limit int) []Row {
	pos := cc.positions(want)
	if offset < 0 {
		offset = 0
	}
	if offset > len(pos) {
		offset = len(pos)
	}
	end := offset + limit
	if limit <= 0 || end > len(pos) {
		end = len(pos)
	}
	out := make([]Row, 0, end-offset)
	for k := offset; k < end; k++ {
		if row, ok := cc.row(ix, pos[k]); ok {
			out = append(out, row)
		}
	}
	return out
}
