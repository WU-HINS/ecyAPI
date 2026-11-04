package store

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAggregateDistributionLocked 锁定父目录聚合的抽取分布。
//
// 这是 P2b（aggKids → aggUnit）重构的**对照基准**：
// 重构前后必须都通过，否则说明抽取权重变了。
func TestAggregateDistributionLocked(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "categories")
	// 三层不同规模的分类，父目录 p 下聚合
	sizes := map[string]int{"p/small": 5, "p/mid": 50, "p/big": 200}
	for rel, n := range sizes {
		for i := 0; i < n; i++ {
			writePNG(t, filepath.Join(dir, filepath.FromSlash(rel), "f"+pad(i)+".png"), 8, 8)
		}
	}
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer st.(*fileStore).Close()

	const draws = 20000
	counts := map[string]int{}
	for i := 0; i < draws; i++ {
		rows, err := st.RandomWith(context.Background(), "p", 1, OrientationAny)
		if err != nil || len(rows) == 0 {
			t.Fatalf("抽取失败: %v", err)
		}
		// link 形如 /pic/p/small/f000.png
		parts := strings.Split(strings.TrimPrefix(rows[0].Link, "/pic/"), "/")
		if len(parts) < 3 {
			t.Fatalf("link 结构异常: %s", rows[0].Link)
		}
		counts[parts[0]+"/"+parts[1]]++
	}

	// 期望权重：sqrt(n) + floor（与 aggregate.go 口径一致）
	total := 0.0
	want := map[string]float64{}
	for cat, n := range sizes {
		w := math.Sqrt(float64(n)) + aggFloorWeight
		want[cat] = w
		total += w
	}
	for cat, w := range want {
		exp := w / total * draws
		got := float64(counts[cat])
		dev := math.Abs(got-exp) / exp
		t.Logf("%-10s 期望 %7.0f 实测 %6d 偏差 %.2f%%", cat, exp, counts[cat], dev*100)
		if dev > 0.15 {
			t.Fatalf("%s 分布偏差过大: 期望 %.0f 实测 %d（%.1f%%）", cat, exp, counts[cat], dev*100)
		}
	}
}
