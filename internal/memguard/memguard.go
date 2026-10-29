// Package memguard 按容器内存限制做「软目标 + 硬红线」的运行时调节。
//
// 目标：
//   - 高占用时把 RSS 稳定在容器限制的 ~75%；
//   - 从软目标（75%）到红线（90%）之间**按比例收紧**：越接近红线，
//     允许的并发/预算越小、内存归还越频繁（Pressure 0→1）；
//   - 达到红线触发紧急回收：进一步压 GOMEMLIMIT、加密 FreeOSMemory、
//     并让大分配方（图库、缩略图）直接降级，避免 Go 的软限被瞬时分配顶穿；
//   - 空闲时把页还给 OS，但严格节流，避免频繁 FreeOSMemory 造成的 GC/IO 抖动。
//
// 读 RSS 用 /proc/self/statm（一次系统调用），读堆用 runtime/metrics（不停世界）。
package memguard

import (
	"log"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Level 内存压力档位。
type Level int32

const (
	// Normal 正常：允许用满软目标。
	Normal Level = iota
	// Warn 偏高：缩小软限、压低 GOGC，上层应减少可以缓一缓的工作（如缩略图压缩）。
	Warn
	// Critical 逼近红线：进一步压限并主动归还，上层应拒绝非核心请求。
	Critical
)

func (l Level) String() string {
	switch l {
	case Warn:
		return "warn"
	case Critical:
		return "critical"
	}
	return "normal"
}

// Governor 运行时内存调节器。
type Governor struct {
	limit   int64   // 容器内存限制（字节）
	softPct float64 // 软目标，默认 0.75
	hardPct float64 // 红线，默认 0.90

	level       atomic.Int32
	ratioBits   atomic.Uint64 // 最近一次占用比例（math.Float64bits）
	lastReq     atomic.Int64  // 最近一次请求时间（UnixNano），用于判断空闲
	lastFree    atomic.Int64  // 上次 FreeOSMemory（UnixNano）
	appliedOnce bool          // 首次 Tick 也要把当前档位的参数落下去

	// 可注入，便于测试
	readRSS  func() int64
	readHeap func() int64
	apply    func(limit int64, gcPercent int)
	freeOS   func()
}

// Options 构造参数。
type Options struct {
	LimitBytes int64   // 容器限制；<=0 表示未知
	SoftPct    float64 // 软目标百分比（0.75）
	HardPct    float64 // 红线百分比（0.90）
}

// New 创建调节器。
func New(opt Options) *Governor {
	g := &Governor{
		limit:   opt.LimitBytes,
		softPct: opt.SoftPct,
		hardPct: opt.HardPct,
	}
	if g.limit <= 0 {
		g.limit = 512 << 20
	}
	if g.softPct <= 0 || g.softPct >= 1 {
		g.softPct = 0.75
	}
	if g.hardPct <= g.softPct || g.hardPct > 1 {
		g.hardPct = 0.90
	}
	g.readRSS = readRSS
	g.readHeap = readHeap
	g.apply = func(limit int64, gcPercent int) {
		debug.SetMemoryLimit(limit)
		debug.SetGCPercent(gcPercent)
	}
	g.freeOS = debug.FreeOSMemory
	g.NoteRequest() // 启动即视为活跃，避免刚启动就触发回收
	return g
}

// Limit 容器内存限制。
func (g *Governor) Limit() int64 { return g.limit }

// Level 当前压力档位。
func (g *Governor) Level() Level { return Level(g.level.Load()) }

// NoteRequest 记录一次请求（由中间件调用，热路径只做一次原子写）。
func (g *Governor) NoteRequest() { g.lastReq.Store(time.Now().UnixNano()) }

// Pressure 返回软目标到红线之间的压力系数：0 = 未超过软目标，1 = 已到红线。
// 上层用它按比例收紧并发/预算，而不是等跨档才动作。
func (g *Governor) Pressure() float64 {
	r := g.Ratio()
	p := (r - g.softPct) / (g.hardPct - g.softPct)
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// Permits 返回当前允许的「大操作并发数」（基准 4）。
// 越接近红线越少：p=0 → 4，p≈0.5 → 2，p≈0.8 → 1，p≥0.9 → 0（禁止新的大分配）。
func (g *Governor) Permits() int {
	const base = 4
	p := g.Pressure()
	if p >= 0.9 {
		return 0
	}
	if p <= 0 {
		return base
	}
	n := int(float64(base) * (1 - p*0.9))
	if n < 1 {
		n = 1
	}
	return n
}

// ScaleMP 按压力把像素预算按比例缩小；压力到红线返回 0（禁止压缩）。
func (g *Governor) ScaleMP(base int) int {
	p := g.Pressure()
	if p >= 1 {
		return 0
	}
	scaled := int(float64(base) * (1 - p))
	if scaled < 1 {
		scaled = 1
	}
	return scaled
}

// Allow 判断在当前占用下能否再分配约 need 字节。
//
// 这是「事件级」保护：Tick 之间（最多 1 秒）可能出现瞬时大分配，
// 大分配方（图库渲染、缩略图解码）在动手前问一句，能有效避免越线。
func (g *Governor) Allow(need int64) bool {
	if need <= 0 {
		need = 1
	}
	rss := g.readRSS()
	budget := int64(float64(g.limit) * g.hardPct)
	return rss+need <= budget
}

// Ratio 当前占用比例（RSS 与堆取较大者 / 限制）。
func (g *Governor) Ratio() float64 {
	rss, heap := g.readRSS(), g.readHeap()
	used := rss
	if heap > used {
		used = heap
	}
	return float64(used) / float64(g.limit)
}

// Tick 执行一次调节（Run 每 5 秒调用；测试可直接调用）。
func (g *Governor) Tick() {
	ratio := g.Ratio()
	g.ratioBits.Store(math.Float64bits(ratio))
	prev := g.Level()
	next := prev

	switch {
	case ratio >= g.hardPct:
		next = Critical
	case ratio >= g.softPct+0.05: // 带 5 个百分点的回滞，避免在阈值附近抖动
		if prev == Critical {
			next = Critical
		} else {
			next = Warn
		}
	case ratio < g.softPct:
		next = Normal
	}

	if next != prev || !g.appliedOnce {
		g.appliedOnce = true
		g.level.Store(int32(next))
		log.Printf("内存压力: %s（占用 %.0f%% / 预算 %d MB）", next, ratio*100, g.limit>>20)
		switch next {
		case Critical:
			// 红线：紧急回收——软限压到 50%，GOGC 压到 15，并且每 5 秒就想归还一次。
			g.apply(g.limit*50/100, 15)
			g.maybeFreeOS(5 * time.Second)
		case Warn:
			// 偏高：软限压到 65%，GOGC 40。
			g.apply(g.limit*65/100, 40)
		default:
			// 正常：软限 70%，让 RSS 稳定在限制的 ~75%。
			g.apply(g.limit*70/100, 50)
		}
	}

	// 压力越大，归还越频繁（但都有下限，避免 GC/IO 抖动）：
	//   p≥0.5 → 每 15s；p≥0.8 → 每 8s；到红线由上面 Critical 分支按 5s 处理。
	p := g.Pressure()
	switch {
	case p >= 0.8:
		g.maybeFreeOS(8 * time.Second)
	case p >= 0.5:
		g.maybeFreeOS(15 * time.Second)
	}

	// 空闲回收：30 秒没有请求、且有明显堆占用时，最多每分钟归还一次。
	if g.Level() == Normal {
		idle := time.Since(time.Unix(0, g.lastReq.Load())) > 30*time.Second
		if idle && g.readHeap() > 64<<20 {
			g.maybeFreeOS(60 * time.Second)
		}
	}
}

// maybeFreeOS 节流归还：两次 FreeOSMemory 至少间隔 min。
// FreeOSMemory 会触发一次完整 GC 并 madvise 归还页，频率必须压住，
// 否则就是持续 CPU 抖动（有 swap 时还会带来 IO）。
func (g *Governor) maybeFreeOS(min time.Duration) {
	now := time.Now()
	last := time.Unix(0, g.lastFree.Load())
	if now.Sub(last) < min {
		return
	}
	g.lastFree.Store(now.UnixNano())
	g.freeOS()
}

// Run 后台循环，每 interval 调一次 Tick；interval<=0 取 5 秒。
func (g *Governor) Run(interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		g.Tick()
	}
}

// readRSS 读 /proc/self/statm 的常驻页数。读不到返回 0。
func readRSS() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize())
}

// heapSample 复用的指标采样，避免每次分配。
var heapSample = []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}

// readHeap 读当前堆上对象字节数（runtime/metrics 不停世界）。
func readHeap() int64 {
	metrics.Read(heapSample)
	if heapSample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return int64(heapSample[0].Value.Uint64())
}

var _ = runtime.GOOS
