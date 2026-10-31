package server

// pick.go — 分类级的加权随机。
//
// /@img（全分类）与 /@img/<name>（虚拟分组）原来是「成员之间平权」，
// 这会让 5 万张的大分类和 100 张的小分类命中率一样。现在统一用
// sqrt(图片数)+底分 的权重（与父目录聚合同一套函数），既能体现规模差异，
// 又保证小分类还有被抽到的机会。

import (
	"context"
	"math/rand/v2"

	"ecyapi/internal/store"
)

// weightedPick 从候选里按权重挑一个；tried 里的会被跳过。
// counts 是「分类 → 图片数」，聚合类路径由调用方预先算好。
func weightedPick(cats []string, counts map[string]int, tried map[string]bool) (string, bool) {
	total := 0.0
	for _, c := range cats {
		if tried[c] {
			continue
		}
		total += store.UnitWeight(counts[c])
	}
	if total <= 0 {
		return "", false
	}
	r := rand.Float64() * total
	last := ""
	for _, c := range cats {
		if tried[c] {
			continue
		}
		last = c
		r -= store.UnitWeight(counts[c])
		if r <= 0 {
			return c, true
		}
	}
	return last, last != ""
}

// countsFor 计算候选路径的图片数（分类与父聚合都能算）。
func (s *Server) countsFor(ctx context.Context, cats []string) map[string]int {
	out := make(map[string]int, len(cats))
	cached := s.st.Counts(ctx)
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
