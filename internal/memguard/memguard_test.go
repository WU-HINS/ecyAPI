package memguard

import (
	"sync"
	"testing"
	"time"
)

// fake 构造一个可注入读数的 Governor，用于验证分档与调节。
func fake(limit int64) (*Governor, *struct {
	mu       sync.Mutex
	rss      int64
	heap     int64
	applied  int64
	gcPct    int
	freeN    int
	lastFree time.Time
}) {
	type state = struct {
		mu       sync.Mutex
		rss      int64
		heap     int64
		applied  int64
		gcPct    int
		freeN    int
		lastFree time.Time
	}
	st := &state{}
	g := New(Options{LimitBytes: limit, SoftPct: 0.75, HardPct: 0.90})
	g.readRSS = func() int64 { st.mu.Lock(); defer st.mu.Unlock(); return st.rss }
	g.readHeap = func() int64 { st.mu.Lock(); defer st.mu.Unlock(); return st.heap }
	g.apply = func(l int64, p int) { st.mu.Lock(); st.applied, st.gcPct = l, p; st.mu.Unlock() }
	g.freeOS = func() { st.mu.Lock(); st.freeN++; st.lastFree = time.Now(); st.mu.Unlock() }
	return g, st
}

// TestGovernorLevels 验证 75% 软目标 / 90% 红线的分档与调节参数。
func TestGovernorLevels(t *testing.T) {
	const limit = 100 << 20
	g, st := fake(limit)

	// 50%：正常档，GOMEMLIMIT=70%，GOGC=50
	st.mu.Lock()
	st.rss = 50 << 20
	st.mu.Unlock()
	g.Tick()
	if g.Level() != Normal {
		t.Fatalf("50%% 应为 Normal，得到 %v", g.Level())
	}
	st.mu.Lock()
	gotLimit, gotGC := st.applied, st.gcPct
	st.mu.Unlock()
	if gotLimit != limit*70/100 || gotGC != 50 {
		t.Fatalf("Normal 档调节 = %d/%d，期望 %d/50", gotLimit, gotGC, limit*70/100)
	}

	// 82%：Warn（>75+5 回滞），压到 65%/40
	st.mu.Lock()
	st.rss = 82 << 20
	st.mu.Unlock()
	g.Tick()
	if g.Level() != Warn {
		t.Fatalf("82%% 应为 Warn，得到 %v", g.Level())
	}
	st.mu.Lock()
	gotLimit, gotGC = st.applied, st.gcPct
	st.mu.Unlock()
	if gotLimit != limit*65/100 || gotGC != 40 {
		t.Fatalf("Warn 档调节 = %d/%d", gotLimit, gotGC)
	}

	// 回滞：从 Critical 掉到 82% 仍应保持 Critical
	st.mu.Lock()
	st.rss = 95 << 20
	st.mu.Unlock()
	g.Tick()
	if g.Level() != Critical {
		t.Fatalf("95%% 应为 Critical")
	}
	st.mu.Lock()
	gotLimit, gotGC = st.applied, st.gcPct
	st.mu.Unlock()
	if gotLimit != limit*55/100 || gotGC != 20 {
		t.Fatalf("Critical 档调节 = %d/%d", gotLimit, gotGC)
	}

	st.mu.Lock()
	st.rss = 82 << 20
	st.mu.Unlock()
	g.Tick()
	if g.Level() != Critical {
		t.Fatalf("回滞：82%% 时仍应为 Critical，得到 %v", g.Level())
	}

	// 降回 60%：回到 Normal
	st.mu.Lock()
	st.rss = 60 << 20
	st.mu.Unlock()
	g.Tick()
	if g.Level() != Normal {
		t.Fatalf("60%% 应回到 Normal，得到 %v", g.Level())
	}
}

// TestGovernorFreeOSThrottle 归还内存必须节流，避免频繁 GC/IO。
func TestGovernorFreeOSThrottle(t *testing.T) {
	const limit = 100 << 20
	g, st := fake(limit)
	st.mu.Lock()
	st.rss = 95 << 20
	st.mu.Unlock()
	g.Tick() // 进入 Critical 触发一次
	st.mu.Lock()
	n := st.freeN
	st.mu.Unlock()
	if n != 1 {
		t.Fatalf("进入 Critical 应归还一次，得到 %d", n)
	}
	// 保持在 Critical 反复 Tick，不应重复归还（30s 节流）
	for i := 0; i < 5; i++ {
		g.Tick()
	}
	st.mu.Lock()
	n = st.freeN
	st.mu.Unlock()
	if n != 1 {
		t.Fatalf("节流失效：FreeOSMemory 被调用 %d 次", n)
	}
}

// TestGovernorIdleReclaim 空闲且堆较大时归还一次，且不频繁。
func TestGovernorIdleReclaim(t *testing.T) {
	const limit = 512 << 20
	g, st := fake(limit)
	st.mu.Lock()
	st.rss, st.heap = 200<<20, 200<<20
	st.mu.Unlock()
	g.Tick() // 首次：Normal，不空闲，不归还
	st.mu.Lock()
	if st.freeN != 0 {
		t.Fatalf("忙碌时不应归还，得到 %d", st.freeN)
	}
	st.mu.Unlock()

	// 伪造 40 秒无请求
	g.lastReq.Store(time.Now().Add(-40 * time.Second).UnixNano())
	g.Tick()
	st.mu.Lock()
	n := st.freeN
	st.mu.Unlock()
	if n != 1 {
		t.Fatalf("空闲时应归还一次，得到 %d", n)
	}
	// 立刻再 Tick：仍应节流
	g.Tick()
	st.mu.Lock()
	if st.freeN != 1 {
		t.Fatalf("应节流，FreeOSMemory 次数 %d", st.freeN)
	}
	st.mu.Unlock()
}
