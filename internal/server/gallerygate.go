package server

import (
	"context"
	"runtime/debug"
	"sync"
	"time"
)

// galleryGate 图库页的并发闸门。
//
// 图库的初衷是「展示某个分类的全部图片」，所以不能像列表页那样采样；
// 但一次渲染 5 万张要 ~8MB，几十并发就会把内存打穿。
// 折中：允许排队，但限制同时渲染的数量（默认 2），排队超时则返回 503。
// 这样内存上限 = 并发数 × 单次渲染开销，与访问量无关。
type galleryGate struct {
	sem      chan struct{}
	wait     time.Duration
	mu       sync.Mutex
	lastFree time.Time
}

func newGalleryGate(concurrency int, wait time.Duration) *galleryGate {
	if concurrency < 1 {
		concurrency = 1
	}
	if wait <= 0 {
		wait = 5 * time.Second
	}
	return &galleryGate{sem: make(chan struct{}, concurrency), wait: wait}
}

// tryAcquire 非阻塞获取：内存压力高时不希望请求在队里占着连接等待。
func (g *galleryGate) tryAcquire() bool {
	select {
	case g.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// acquire 尝试获得渲染名额：有空位立即返回，否则最多等 wait。
func (g *galleryGate) acquire(ctx context.Context) bool {
	select {
	case g.sem <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(g.wait)
	defer t.Stop()
	select {
	case g.sem <- struct{}{}:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// release 归还名额；完全空闲时节流把堆页还给操作系统。
func (g *galleryGate) release() {
	<-g.sem
	g.mu.Lock()
	idle := len(g.sem) == 0 && time.Since(g.lastFree) > 30*time.Second
	if idle {
		g.lastFree = time.Now()
	}
	g.mu.Unlock()
	if idle {
		go debug.FreeOSMemory()
	}
}
