package memguard

import (
	"bytes"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
)

type fakeState struct {
	mu      sync.Mutex
	rss     int64
	heap    int64
	applied int64
	gcPct   int
	freeN   int
}

// fake 构造一个可注入读数的 Governor。
func fake(limit int64) (*Governor, *fakeState) {
	st := &fakeState{}
	g := New(Options{LimitBytes: limit, SoftPct: 0.02, HardPct: 0.80, TargetCap: 0.30})
	g.readRSS = func() int64 { st.mu.Lock(); defer st.mu.Unlock(); return st.rss }
	g.readHeap = func() int64 { st.mu.Lock(); defer st.mu.Unlock(); return st.heap }
	g.apply = func(l int64, p int) { st.mu.Lock(); st.applied, st.gcPct = l, p; st.mu.Unlock() }
	g.freeOS = func() { st.mu.Lock(); st.freeN++; st.mu.Unlock() }
	return g, st
}

func setRSS(st *fakeState, v int64) {
	st.mu.Lock()
	st.rss = v
	st.mu.Unlock()
}

func applied(st *fakeState) (int64, int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.applied, st.gcPct
}

// TestDefaultTarget 目标口径：min(25MB, 2% × 限额) 起步，按图片数放宽，上限 30%。
func TestDefaultTarget(t *testing.T) {
	const mb = int64(1 << 20)
	cases := []struct {
		limit  int64
		images int
		want   int64
	}{
		{2048 * mb, 0, 2048 * mb * 2 / 100},    // 2% = 41MB > 25MB，按 2% 取
		{512 * mb, 0, 25 * mb},                 // 2% = 10MB < 25MB → 仍取 25MB（进程要跑起来）
		{512 * mb, 50000, 512 * mb * 30 / 100}, // 30% × 512MB
		{256 * mb, 50000, 256 * mb * 30 / 100}, // 30% × 256MB
	}
	for _, c := range cases {
		if got := DefaultTarget(c.limit, c.images); got != c.want {
			t.Fatalf("DefaultTarget(%dMB, %d) = %dMB，期望 %dMB", c.limit>>20, c.images, got>>20, c.want>>20)
		}
	}
	// 单调不减
	prev := int64(0)
	for _, n := range []int{0, 1000, 5000, 20000, 50000} {
		v := DefaultTarget(512*mb, n)
		if v < prev {
			t.Fatalf("目标应随图片数单调不减：n=%d 得到 %dMB < 之前 %dMB", n, v>>20, prev>>20)
		}
		prev = v
	}
}

// TestGovernorLevels 目标驱动的分档：达标 / 超标 / 触顶。
func TestGovernorLevels(t *testing.T) {
	const limit = 512 << 20
	g, st := fake(limit)
	g.SetTarget(25 << 20) // 目标 25MB，上限 30% = 153MB

	setRSS(st, 20<<20)
	g.Tick()
	if g.Level() != Normal {
		t.Fatalf("20MB 应为 Normal，得到 %v", g.Level())
	}
	if lim, gc := applied(st); lim != (25<<20)+(25<<20)/10 || gc != 50 {
		t.Fatalf("Normal 调节 = %d/%d", lim, gc)
	}

	setRSS(st, 40<<20)
	g.Tick()
	if g.Level() != Warn {
		t.Fatalf("40MB 应为 Warn，得到 %v", g.Level())
	}
	if lim, gc := applied(st); lim != 25<<20 || gc != 30 {
		t.Fatalf("Warn 调节 = %d/%d", lim, gc)
	}

	setRSS(st, 420<<20)
	g.Tick()
	if g.Level() != Critical {
		t.Fatalf("420MB 应为 Critical，得到 %v", g.Level())
	}
	if lim, gc := applied(st); lim != (25<<20)*90/100 || gc != 15 {
		t.Fatalf("Critical 调节 = %d/%d", lim, gc)
	}

	setRSS(st, 10<<20)
	g.Tick()
	if g.Level() != Normal {
		t.Fatalf("回落应 Normal，得到 %v", g.Level())
	}
}

// TestPressureAndPermits 压力从目标到上限线性增长。
func TestPressureAndPermits(t *testing.T) {
	g, st := fake(512 << 20)
	g.SetTarget(25 << 20)

	setRSS(st, 10<<20)
	if p := g.Pressure(); p != 0 {
		t.Fatalf("目标以下压力应为 0，得到 %v", p)
	}
	if n := g.Permits(); n != 4 {
		t.Fatalf("低压力应允许 4 并发，得到 %d", n)
	}
	if !g.Allow(8 << 20) {
		t.Fatal("目标一半以内应允许分配")
	}

	setRSS(st, 420<<20)
	if p := g.Pressure(); p < 0.99 {
		t.Fatalf("触顶压力应≈1，得到 %v", p)
	}
	if n := g.Permits(); n != 0 {
		t.Fatalf("触顶应禁止大操作，得到 %d", n)
	}
}

// TestSetTargetZeroFallsBack 未设置目标时按百分比兜底。
func TestSetTargetZeroFallsBack(t *testing.T) {
	g, _ := fake(100 << 20)
	g.SetTarget(0)
	if got := g.Target(); got != 25<<20 {
		t.Fatalf("兜底目标应为 25MB 下限，实际 %dMB", got>>20)
	}
	g.SetTarget(25 << 20)
	if got := g.Target(); got != 25<<20 {
		t.Fatalf("显式目标 = %dMB", got>>20)
	}
}

// TestTargetForQPS 高 QPS 放宽目标，但不超过 30% 上限；阈值 0 表示关闭。
func TestTargetForQPS(t *testing.T) {
	const mb = int64(1 << 20)
	limit := int64(512 * mb)
	base := TargetFor(limit, 0, 0, 100)
	if base != 25*mb {
		t.Fatalf("base = %dMB，期望 25MB", base>>20)
	}
	if got := TargetFor(limit, 0, 50, 100); got != base {
		t.Fatalf("50 QPS 不应放宽，得到 %dMB", got>>20)
	}
	if got := TargetFor(limit, 0, 400, 100); got != limit*30/100 {
		t.Fatalf("400 QPS 应放宽到上限，得到 %dMB", got>>20)
	}
	mid := TargetFor(limit, 0, 250, 100)
	if mid <= base || mid >= limit*30/100 {
		t.Fatalf("250 QPS 应介于中间，得到 %dMB", mid>>20)
	}
	if got := TargetFor(limit, 0, 100000, 0); got != base {
		t.Fatalf("阈值 0 时不应放宽，得到 %dMB", got>>20)
	}
	if got := TargetFor(limit, 50000, 0, 100); got != limit*30/100 {
		t.Fatalf("5 万张应到上限，得到 %dMB", got>>20)
	}
}

// TestSetTargetLogged 目标变化要留痕：显著变化记一条，微调不刷屏。
func TestSetTargetLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	g, _ := fake(512 << 20)
	g.SetTarget(25 << 20)
	buf.Reset()

	g.SetTargetLogged(146<<20, "索引 50000 张")
	if !strings.Contains(buf.String(), "内存目标调整") || !strings.Contains(buf.String(), "索引 50000 张") {
		t.Fatalf("目标显著变化应记日志，实际 %q", buf.String())
	}

	buf.Reset()
	g.SetTargetLogged(146<<20+1<<20, "微调")
	if buf.Len() != 0 {
		t.Fatalf("小幅变化不应刷屏，实际 %q", buf.String())
	}
}
