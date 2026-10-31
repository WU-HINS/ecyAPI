package server

// activity.go — 每 N 秒输出一行「当前活跃任务」日志。
//
// 目的：内存飙高时能立刻分辨是「自己在干活」（图库渲染 / 缩略图压缩 / gzip / 预热 / 扫描）
// 还是「刚好撞上某个请求」。空闲且内存不高时不输出，避免刷屏。

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ecyapi/internal/memguard"
	"ecyapi/internal/store"
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

	inflight atomic.Int64  // 正在处理的 HTTP 请求数（含图片）
	served   atomic.Int64  // 累计完成请求数（用于算速率）
	lastSeen atomic.Int64  // 上一轮日志时的 served 值
	qpsBase  atomic.Int64  // 上一轮 QPS 计算基准
	qpsBits  atomic.Uint64 // 当前 QPS（math.Float64bits）
	qpsEval  atomic.Uint64 // 访问曲线评估出的 QPS
	curveMu  sync.Mutex
	curve    accessCurve
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
		// 按索引规模 + 近期 QPS 刷新内存目标（下次 Tick 生效）。
		// QPS 高说明正在服务大量用户：适当放宽目标换取体验，但仍受 memguard 红线约束。
		qps := s.tickQPS(interval)
		if n, ok := s.totalImages(); ok {
			target := memguard.TargetFor(s.mem.Limit(), n, qps, float64(s.cfg.MemQPSThreshold))
			s.mem.SetTargetLogged(target, s.targetReason(n, qps))
		}
		if line, ok := s.activityLine(); ok {
			log.Print(line)
		}
	}
}

// accessCurve 访问曲线：记录最近 40 个采样档（3 秒/档 ≈ 2 分钟）的 QPS，
// 用来评估「这是不是一波持续流量」，而不是被单个瞬间的尖峰带跑。
type accessCurve struct {
	hist [40]float64
	idx  int
	n    int
}

func (c *accessCurve) push(qps float64) {
	c.hist[c.idx] = qps
	c.idx = (c.idx + 1) % len(c.hist)
	if c.n < len(c.hist) {
		c.n++
	}
}

// eval 评估 QPS：最近 5 档（15 秒）均值与当前值各占一半，
// 再与近期峰值（最近 20 档）取较大者的一半，避免瞬时尖峰直接放宽目标、也避免流量刚起就不放宽。
func (c *accessCurve) eval(cur float64) float64 {
	if c.n == 0 {
		return cur
	}
	var sum float64
	cnt := 5
	if cnt > c.n {
		cnt = c.n
	}
	for i := 1; i <= cnt; i++ {
		sum += c.hist[(c.idx-i+len(c.hist))%len(c.hist)]
	}
	avg := sum / float64(cnt)

	peak := 0.0
	pc := 20
	if pc > c.n {
		pc = c.n
	}
	for i := 1; i <= pc; i++ {
		if v := c.hist[(c.idx-i+len(c.hist))%len(c.hist)]; v > peak {
			peak = v
		}
	}
	// 评估值 = max(近 15 秒均值, 当前值的 50%)：
	// 持续流量按均值算（稳定），单档尖峰只按一半计入（不因瞬时抖动就放宽），
	// 流量回落后均值自然降下来，不会被历史峰值粘住。
	evaluated := avg
	if half := cur * 0.5; half > evaluated {
		evaluated = half
	}
	_ = peak
	return evaluated
}

// tickQPS 采样当前 QPS 并给出「评估值」（基于访问曲线，用于放宽内存目标）。
func (s *Server) tickQPS(interval time.Duration) float64 {
	served := s.act.served.Load()
	prev := s.act.qpsBase.Swap(served)
	cur := 0.0
	if interval > 0 {
		cur = float64(served-prev) / interval.Seconds()
	}
	s.act.curveMu.Lock()
	s.act.curve.push(cur)
	evaluated := s.act.curve.eval(cur)
	s.act.curveMu.Unlock()
	s.act.qpsBits.Store(math.Float64bits(cur))
	s.act.qpsEval.Store(math.Float64bits(evaluated))
	return evaluated
}

// targetReason 说明这次目标是按什么算出来的（写进日志便于事后对账）。
func (s *Server) targetReason(images int, qps float64) string {
	parts := []string{fmt.Sprintf("索引 %d 张", images)}
	if th := float64(s.cfg.MemQPSThreshold); th > 0 && qps > th {
		parts = append(parts, fmt.Sprintf("QPS 放宽 %.0f>%.0f", qps, th))
	} else if th == 0 {
		parts = append(parts, "QPS 放宽已关闭")
	}
	return strings.Join(parts, "，")
}

// totalImages 索引里的图片总数（用于计算自适应内存目标）。
func (s *Server) totalImages() (int, bool) {
	cats, err := s.st.Categories(context.Background())
	if err != nil {
		return 0, false
	}
	total := 0
	for _, c := range cats {
		if n, err := s.st.CountWith(context.Background(), c, store.OrientationAny); err == nil {
			total += n
		}
	}
	return total, true
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
	// 访问曲线评估值：目标放宽看的是它，不是瞬时 QPS
	if ev := math.Float64frombits(a.qpsEval.Load()); ev > 0 {
		parts = append(parts, fmt.Sprintf("QPS 评估 %.0f", ev))
	}
	if len(parts) == 0 {
		parts = append(parts, "无活跃任务")
	}

	return fmt.Sprintf("活跃任务：%s｜堆 %.0f/%.0fMB（%.0f%%）目标 %dMB RSS %.0fMB 压力 %.2f",
		strings.Join(parts, "｜"),
		float64(s.mem.Heap())/1048576, float64(s.mem.Limit())/1048576, ratio*100,
		s.mem.Target()>>20, float64(s.mem.RSS())/1048576, s.mem.Pressure()), true
}
