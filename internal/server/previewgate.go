package server

import (
	"runtime/debug"
	"sync"
	"time"
)

// previewGate 按「解码后位图大小」加权的并发闸门。
//
// 只按请求数限流（比如 25 并发）并不安全：一张 4K 图解码后约 33MB
// （3840×2160×4），25 张同时解码峰值就是 800MB+，小内存容器直接 OOM。
// 这里改为按像素预算限流：4K 图约 8 个预算单位，预算 64 时最多同时 8 张。
// 预算不足时请求直接回退原图（等价于原来的「队列满回退」），永不排队。
type previewGate struct {
	mu       sync.Mutex
	used     int
	cap      int       // 总预算（百万像素）
	lastFree time.Time // 上次归还内存给 OS 的时间，避免频繁 FreeOSMemory
}

func newPreviewGate(capMP int) *previewGate {
	if capMP < 1 {
		capMP = 1
	}
	return &previewGate{cap: capMP}
}

// tryAcquire 申请 n 个预算单位；预算不足返回 false。
func (g *previewGate) tryAcquire(n int) bool {
	n = g.clampUnits(n)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.used+n > g.cap {
		return false
	}
	g.used += n
	return true
}

// acquireWait 在 wait 时间内等待 n 个单位的预算；超时返回 false。
// 供后台补缓存使用：宁可慢一点，也要把缓存填上。
func (g *previewGate) acquireWait(n int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if g.tryAcquire(n) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tryAcquireLow 低优先级获取：只在已用预算低于 fraction（如 0.5）时授予。
// 后台预热用它，保证线上请求始终有至少 (1-fraction) 的预算可用。
func (g *previewGate) tryAcquireLow(n int, fraction float64) bool {
	n = g.clampUnits(n)
	limit := int(float64(g.cap) * fraction)
	if limit < 1 {
		limit = 1
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.used+n > limit {
		return false
	}
	g.used += n
	return true
}

// release 归还预算；若已完全空闲，按节流把空闲堆页还给操作系统。
func (g *previewGate) release(n int) {
	n = g.clampUnits(n)
	g.mu.Lock()
	g.used -= n
	if g.used < 0 {
		g.used = 0
	}
	idle := g.used == 0
	free := idle && time.Since(g.lastFree) > 30*time.Second
	if free {
		g.lastFree = time.Now()
	}
	g.mu.Unlock()
	if free {
		// 异步归还，避免把 GC 停顿加到响应延迟上。
		go debug.FreeOSMemory()
	}
}

func (g *previewGate) clampUnits(n int) int {
	if n < 1 {
		return 1
	}
	if n > g.cap {
		return g.cap
	}
	return n
}

// previewUnits 把像素数换算成预算单位（百万像素，向上取整，最小 1）。
func previewUnits(w, h int) int {
	px := int64(w) * int64(h)
	if px <= 0 {
		return 1
	}
	u := int(px / 1_000_000)
	if px%1_000_000 != 0 {
		u++
	}
	if u < 1 {
		u = 1
	}
	return u
}
