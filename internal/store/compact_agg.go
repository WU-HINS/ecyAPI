package store

// compact_agg.go —— compact 模式下的「父目录聚合」：直接从索引现算，不常驻。
//
// 动机：内存里的 aggKids 持有 []fileEntry 切片头，会把整个分类的条目数组钉在堆上，
// 导致 drop cache 也释放不掉（实测省 0 MB）。compact 模式下改为：
//   - 不构建 aggKids；
//   - 父目录请求时按前缀扫索引里的分类，按图片数加权抽一条；
//   - 结果不缓存（分类表很小，扫一遍是 O(分类数)），换来的是堆里没有任何条目副本。

import (
	"math"
	"math/rand/v2"
	"strings"

	"ecyapi/internal/index"
)

// compactParent 父目录下参与聚合的分类（前缀匹配，排除自身）。
func (f *fileStore) compactParent(parent string) ([]index.Category, bool) {
	cs := f.compactReady()
	if cs == nil {
		return nil, false
	}
	prefix := parent + "/"
	var out []index.Category
	for _, c := range cs.ix.Categories() {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// compactParentCount 父目录聚合的图片数。
func (f *fileStore) compactParentCount(parent string, want Orientation) (int, bool) {
	cats, ok := f.compactParent(parent)
	if !ok {
		return 0, false
	}
	cs := f.compactReady()
	total := 0
	for _, c := range cats {
		if cc, ok := cs.cats.get(c.Name); ok {
			total += cc.count(want)
		}
	}
	return total, true
}

// compactParentRandom 父目录聚合的加权随机（按 sqrt(图片数)+底分，与内存版口径一致）。
func (f *fileStore) compactParentRandom(parent string, want Orientation, limit int) ([]Row, bool) {
	cats, ok := f.compactParent(parent)
	if !ok {
		return nil, false
	}
	cs := f.compactReady()
	type unit struct {
		cc     *compactCat
		weight float64
	}
	units := make([]unit, 0, len(cats))
	sum := 0.0
	for _, c := range cats {
		cc, ok := cs.cats.get(c.Name)
		if !ok {
			continue
		}
		n := cc.count(want)
		if n == 0 {
			continue
		}
		w := math.Sqrt(float64(n)) + aggFloorWeight
		units = append(units, unit{cc: cc, weight: w})
		sum += w
	}
	if len(units) == 0 || sum <= 0 {
		return nil, false
	}
	rows := make([]Row, 0, limit)
	seen := map[string]struct{}{}
	for tries := 0; len(rows) < limit && tries < limit*32; tries++ {
		r := rand.Float64() * sum
		var picked *compactCat
		for i := range units {
			r -= units[i].weight
			if r <= 0 {
				picked = units[i].cc
				break
			}
		}
		if picked == nil {
			picked = units[len(units)-1].cc
		}
		got := picked.random(cs.ix, want, 1)
		if len(got) == 0 {
			continue
		}
		if _, dup := seen[got[0].Link]; dup {
			continue
		}
		seen[got[0].Link] = struct{}{}
		rows = append(rows, got[0])
	}
	if len(rows) == 0 {
		return nil, false
	}
	return rows, true
}
