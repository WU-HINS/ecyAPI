package server

import (
	"fmt"
	"testing"
	"time"
)

// TestWeightTableBuildCost 量化「建表」成本，判断是否值得落盘缓存。
func TestWeightTableBuildCost(t *testing.T) {
	for _, n := range []int{1000, 10000, 50000} {
		keys := make([]string, n)
		counts := make(map[string]int, n)
		for i := 0; i < n; i++ {
			keys[i] = fmt.Sprintf("cat%05d", i)
			counts[keys[i]] = 10 + i%50
		}
		start := time.Now()
		tbl := buildWeightTable(keys, counts, 1)
		elapsed := time.Since(start)
		// 粗略内存：keys(引用) + cum + weights 各 8B
		mem := n*8*2 + n*16
		t.Logf("n=%d 建表 %v，约 %.1f MB，表项 %d", n, elapsed, float64(mem)/1048576, len(tbl.keys))
	}
}
