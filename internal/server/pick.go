package server

// pick.go — 分类级加权随机（/@img、虚拟分组共用）。
//
// 权重口径与父目录聚合一致：sqrt(图片数) + ECY_AGG_FLOOR。
// 权重表按「索引代次 + 隐藏列表版本 + 分组定义」缓存，请求路径上只做一次二分，
// 不重复遍历上千个分类算 sqrt（否则 1000 分类时 /@img 会明显掉速）。

import (
	"context"
	"math/rand/v2"
	"sort"

	"ecyapi/internal/store"
)

// weightTable 预计算的权重表（只读，按代次缓存）。
type weightTable struct {
	gen  uint64
	keys []string
	cum  []float64
	sum  float64
	w    map[string]float64 // key → 权重（跳过已试项时用）
}

// buildWeightTable 构造权重表。
func buildWeightTable(keys []string, counts map[string]int, gen uint64) *weightTable {
	t := &weightTable{
		gen:  gen,
		keys: make([]string, 0, len(keys)),
		cum:  make([]float64, 0, len(keys)),
		w:    make(map[string]float64, len(keys)),
	}
	for _, k := range keys {
		w := store.UnitWeight(counts[k])
		if w <= 0 {
			continue
		}
		t.sum += w
		t.cum = append(t.cum, t.sum)
		t.keys = append(t.keys, k)
		t.w[k] = w
	}
	return t
}

// pick 按权重随机挑一个，跳过 tried 里的项。
func (t *weightTable) pick(tried map[string]bool) (string, bool) {
	if t == nil || len(t.keys) == 0 || t.sum <= 0 {
		return "", false
	}
	if len(tried) == 0 {
		r := rand.Float64() * t.sum
		i := sort.SearchFloat64s(t.cum, r)
		if i >= len(t.keys) {
			i = len(t.keys) - 1
		}
		return t.keys[i], true
	}
	total := 0.0
	for _, k := range t.keys {
		if !tried[k] {
			total += t.w[k]
		}
	}
	if total <= 0 {
		return "", false
	}
	r := rand.Float64() * total
	last := ""
	for _, k := range t.keys {
		if tried[k] {
			continue
		}
		last = k
		r -= t.w[k]
		if r <= 0 {
			return k, true
		}
	}
	return last, last != ""
}

// allWeightTable 全分类权重表（按代次 + 隐藏版本缓存）。
func (s *Server) allWeightTable(cats []string) *weightTable {
	gen := s.st.Generation()
	s.mu.RLock()
	cached := s.wAll
	ver := s.hiddenVersion
	s.mu.RUnlock()
	if cached != nil && cached.gen == gen && s.wAllHidden == ver {
		return cached
	}
	t := buildWeightTable(cats, s.countsFor(context.Background(), cats), gen)
	s.mu.Lock()
	s.wAll, s.wAllHidden = t, ver
	s.mu.Unlock()
	return t
}

// groupWeightTable 虚拟分组的权重表（按代次 + 分组定义缓存）。
func (s *Server) groupWeightTable(name string, members []string) *weightTable {
	gen := s.st.Generation()
	sig := s.groupsSnapshot().hash
	s.mu.RLock()
	cached, ok := s.wGroups[name]
	s.mu.RUnlock()
	if ok && cached.gen == gen && s.wGroupSig == sig {
		return cached
	}
	t := buildWeightTable(members, s.countsFor(context.Background(), members), gen)
	s.mu.Lock()
	if s.wGroups == nil || s.wGroupSig != sig {
		s.wGroups = make(map[string]*weightTable, 4)
		s.wGroupSig = sig
	}
	s.wGroups[name] = t
	s.mu.Unlock()
	return t
}

// countsFor 返回候选路径的图片数（分类与父聚合都能算）。
// 保守起见复用 store 的代次缓存表本身（只读），只有出现缓存里没有的路径
// （例如父聚合）时才复制一份补充。
func (s *Server) countsFor(ctx context.Context, cats []string) map[string]int {
	cached := s.st.Counts(ctx)
	all := true
	for _, c := range cats {
		if _, ok := cached[c]; !ok {
			all = false
			break
		}
	}
	if all {
		return cached
	}
	out := make(map[string]int, len(cats)+4)
	for _, c := range cats {
		if n, ok := cached[c]; ok {
			out[c] = n
			continue
		}
		if n, err := s.st.CountWith(ctx, c, store.OrientationAny); err == nil {
			out[c] = n
		}
	}
	return out
}
