package server

// activity.go — 每 N 秒输出一行「当前活跃任务」日志。
//
// 目的：内存飙高时能立刻分辨是「自己在干活」（图库渲染 / 缩略图压缩 / gzip / 预热 / 扫描）
// 还是「刚好撞上某个请求」。空闲且内存不高时不输出，避免刷屏。

import (
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

// activityTracker 热路径只做原子加减，不拿锁。
type activityTracker struct {
	renders     atomic.Int64 // 正在渲染的图库页数
	renderKey   atomic.Pointer[string]
	renderSince atomic.Int64 // 最早一次渲染开始的 unix nano

	previews  atomic.Int64 // 正在压缩的缩略图数
	previewPx atomic.Int64 // 正在压缩的总像素（用于估算占用）
	w480      atomic.Int64
	w1280     atomic.Int64
	wOther    atomic.Int64

	gzips atomic.Int64 // 正在生成的 gzip 数

	inflight atomic.Int64 // 正在处理的 HTTP 请求数（含图片）
	served   atomic.Int64 // 累计完成请求数（用于算速率）
	lastSeen atomic.Int64 // 上一轮日志时的 served 值
}

// noteRequestStart 请求进入时调用，返回结束函数。
func (s *Server) noteRequestStart() func() {
	s.act.inflight.Add(1)
	return func() {
		s.act.inflight.Add(-1)
		s.act.served.Add(1)
	}
}

// beginRender 标记一次图库渲染开始，返回结束函数（defer 调用）。
func (s *Server) beginRender(key string) func() {
	a := &s.act
	if a.renders.Add(1) == 1 {
		k := key
		a.renderKey.Store(&k)
		a.renderSince.Store(time.Now().UnixNano())
	}
	return func() { a.renders.Add(-1) }
}

// beginPreview 标记一次缩略图压缩。
func (s *Server) beginPreview(px int64, width int) func() {
	a := &s.act
	a.previews.Add(1)
	a.previewPx.Add(px)
	switch {
	case width <= 480:
		a.w480.Add(1)
	case width <= 1280:
		a.w1280.Add(1)
	default:
		a.wOther.Add(1)
	}
	return func() {
		a.previews.Add(-1)
		a.previewPx.Add(-px)
		switch {
		case width <= 480:
			a.w480.Add(-1)
		case width <= 1280:
			a.w1280.Add(-1)
		default:
			a.wOther.Add(-1)
		}
	}
}

// beginGzip 标记一次图库 gzip 生成。
func (s *Server) beginGzip() func() {
	s.act.gzips.Add(1)
	return func() { s.act.gzips.Add(-1) }
}

// activityLoop 周期性输出活跃任务；interval<=0 表示关闭。
func (s *Server) activityLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		if line, ok := s.activityLine(); ok {
			log.Print(line)
		}
	}
}

// activityLine 组装一行状态；没有活跃任务且内存不高时返回 false（不输出）。
func (s *Server) activityLine() (string, bool) {
	a := &s.act
	renders := a.renders.Load()
	previews := a.previews.Load()
	gzips := a.gzips.Load()
	// 只把「跑了超过 1 秒」的扫描算作活跃任务：默认 3 秒一次的增量刷新很短，
	// 否则日志里会一直挂着「磁盘扫描中」，掩盖真正的大任务。
	scanning := false
	scanFor := time.Duration(0)
	if fs, ok := s.st.(interface{ ScanningFor() time.Duration }); ok {
		if d := fs.ScanningFor(); d > time.Second {
			scanning, scanFor = true, d
		}
	}
	pre := s.prewarmSnapshot()
	ratio := s.mem.Ratio()

	// 请求速率：本轮窗口内完成的请求数
	served := a.served.Load()
	prev := a.lastSeen.Swap(served)
	window := served - prev
	inflight := a.inflight.Load()

	active := renders > 0 || previews > 0 || gzips > 0 || scanning || pre.Running
	if !active && ratio < 0.6 && window == 0 && inflight == 0 {
		return "", false
	}

	var parts []string
	if renders > 0 {
		key := ""
		if p := a.renderKey.Load(); p != nil {
			key = *p
		}
		elapsed := time.Duration(0)
		if since := a.renderSince.Load(); since > 0 {
			elapsed = time.Since(time.Unix(0, since))
		}
		parts = append(parts, fmt.Sprintf("图库渲染 %d（%s，已 %s）", renders, key, elapsed.Round(100*time.Millisecond)))
	}
	if previews > 0 {
		var ws []string
		if n := a.w480.Load(); n > 0 {
			ws = append(ws, fmt.Sprintf("480×%d", n))
		}
		if n := a.w1280.Load(); n > 0 {
			ws = append(ws, fmt.Sprintf("1280×%d", n))
		}
		if n := a.wOther.Load(); n > 0 {
			ws = append(ws, fmt.Sprintf("其它×%d", n))
		}
		mp := float64(a.previewPx.Load()) / 1e6
		parts = append(parts, fmt.Sprintf("缩略图压缩 %d（%s，约 %.1f MP 在解）", previews, strings.Join(ws, "、"), mp))
	}
	if gzips > 0 {
		parts = append(parts, fmt.Sprintf("gzip %d", gzips))
	}
	if pre.Running {
		parts = append(parts, fmt.Sprintf("预热运行中（%d/%d）", pre.Done, pre.Total))
	}
	if scanning {
		parts = append(parts, fmt.Sprintf("磁盘扫描中（已 %s）", scanFor.Round(100*time.Millisecond)))
	}
	// 请求量：即使全是缓存命中，也要能看出「是不是正好赶上流量」
	if inflight > 0 || window > 0 {
		parts = append(parts, fmt.Sprintf("请求 处理中 %d、近 3 秒完成 %d", inflight, window))
	}
	if len(parts) == 0 {
		parts = append(parts, "无活跃任务")
	}

	return fmt.Sprintf("活跃任务：%s｜堆 %.0f/%.0fMB（%.0f%%）RSS %.0fMB 压力 %.2f",
		strings.Join(parts, "｜"),
		float64(s.mem.Heap())/1048576, float64(s.mem.Limit())/1048576, ratio*100,
		float64(s.mem.RSS())/1048576, s.mem.Pressure()), true
}
