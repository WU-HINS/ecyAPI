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
	softPct float64 // 软目标百分比（未设置绝对目标时使用）
	hardPct float64 // 红线百分比（降级触发线，默认 85%）
	capPct  float64 // 自适应目标的最高比例（默认 30%）

	// target 绝对内存目标（字节）。0 表示沿用 softPct*limit。
	// 由 SetTarget 按索引规模动态更新：图越少目标越低，最多放宽到 hardPct*limit。
	target atomic.Int64

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
	SoftPct    float64 // 软目标百分比（未设置绝对目标时兜底）
	HardPct    float64 // 降级红线百分比（0.85）
	TargetCap  float64 // 自适应目标上限百分比（0.30）
}

// New 创建调节器。
func New(opt Options) *Governor {
	g := &Governor{
		limit:   opt.LimitBytes,
		softPct: opt.SoftPct,
		hardPct: opt.HardPct,
		capPct:  opt.TargetCap,
	}
	if g.limit <= 0 {
		g.limit = 512 << 20
	}
	if g.softPct <= 0 || g.softPct >= 1 {
		g.softPct = 0.75
	}
	if g.hardPct <= 0 || g.hardPct > 1 {
		g.hardPct = 0.85
	}
	if g.capPct <= 0 || g.capPct > 1 {
		g.capPct = 0.30
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

// DefaultTarget 计算内存目标：
//
//	base = max(25MB, 2% × limit)        // 25MB 是跑起来的最低要求
//	target = base + (30%×limit − base) × max(索引比例, QPS 比例)
//
// 索引比例 = min(图片数, 50000)/50000；QPS 比例为 0（未启用）。
func DefaultTarget(limit int64, images int) int64 {
	return TargetFor(limit, images, 0, 0)
}

// TargetFor 在 DefaultTarget 的基础上叠加「高 QPS 放宽」：
// QPS 超过 threshold 后线性放宽，到 4×threshold 放宽到上限。
// qpsThreshold <= 0 表示不按 QPS 放宽。
func TargetFor(limit int64, images int, qps, qpsThreshold float64) int64 {
	const base25 = 25 << 20
	base := int64(limit * 2 / 100)
	if base < base25 {
		base = base25 // 进程自身就要 25MB 才跑得动
	}
	ceiling := int64(float64(limit) * 0.30)
	if base > ceiling {
		base = ceiling
	}
	if base == ceiling {
		return base
	}

	// 索引比例
	indexFrac := 0.0
	if images > 0 {
		if images >= 50000 {
			indexFrac = 1
		} else {
			indexFrac = float64(images) / 50000
		}
	}
	// QPS 比例：threshold 起放宽，4×threshold 满
	qpsFrac := 0.0
	if qpsThreshold > 0 && qps > qpsThreshold {
		qpsFrac = (qps - qpsThreshold) / (qpsThreshold * 3)
		if qpsFrac > 1 {
			qpsFrac = 1
		}
	}
	frac := indexFrac
	if qpsFrac > frac {
		frac = qpsFrac
	}
	return base + int64(float64(ceiling-base)*frac)
}

// SetTarget 设置绝对内存目标（<=0 表示恢复按百分比推导）。
func (g *Governor) SetTarget(bytes int64) {
	if bytes < 0 {
		bytes = 0
	}
	g.target.Store(bytes)
}

// Target 当前内存目标（字节）。未显式设置时按「25MB 下限 + softPct」兜底。
func (g *Governor) Target() int64 {
	if t := g.target.Load(); t > 0 {
		return t
	}
	return TargetFor(g.limit, 0, 0, 0)
}

// ceiling 降级红线（触发 Critical 的占用线）。
// 注意：它不是「目标上限」——目标最高只到 targetCapPct（30%），
// 而红线要留出足够空间，否则正常运行（如 CI 里带 race 的进程）会被误判为触顶。
func (g *Governor) ceiling() int64 {
	return int64(float64(g.limit) * g.hardPct)
}

// targetCap 自适应目标的最高值（默认 30% × limit）。
func (g *Governor) targetCap() int64 {
	c := int64(float64(g.limit) * g.targetCapPct())
	return c
}

func (g *Governor) targetCapPct() float64 {
	if g.capPct <= 0 || g.capPct > 1 {
		return 0.30
	}
	return g.capPct
}

// Level 当前压力档位。
func (g *Governor) Level() Level { return Level(g.level.Load()) }

// NoteRequest 记录一次请求（由中间件调用，热路径只做一次原子写）。
func (g *Governor) NoteRequest() { g.lastReq.Store(time.Now().UnixNano()) }

// RecentlyActive 判断最近 d 时间内是否有请求。低优先级任务（预热）用它让路。
func (g *Governor) RecentlyActive(d time.Duration) bool {
	return time.Since(time.Unix(0, g.lastReq.Load())) < d
}

// Pressure 返回软目标到红线之间的压力系数：0 = 未超过软目标，1 = 已到红线。
// 上层用它按比例收紧并发/预算，而不是等跨档才动作。
func (g *Governor) Pressure() float64 {
	// 压力按「当前占用 vs 目标 vs 上限」算：目标以下为 0，到上限为 1。
	used := float64(g.used())
	target := float64(g.Target())
	ceil := float64(g.ceiling())
	if used <= target || ceil <= target {
		return 0
	}
	p := (used - target) / (ceil - target)
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
	// 准入只看红线（30%）：目标只决定「GC 多积极、预算收多少」，
	// 不是硬性拒绝线——Go 运行时自身就有 ~20MB 常驻，用小目标硬卡会直接卡死功能。
	return g.used()+need <= g.ceiling()
}

// RSS 当前进程常驻内存（字节），供状态日志使用。
func (g *Governor) RSS() int64 { return g.readRSS() }

// Heap 当前 Go 堆占用（字节）。
func (g *Governor) Heap() int64 { return g.readHeap() }

// used 当前占用：RSS 与堆取较大者。
func (g *Governor) used() int64 {
	rss, heap := g.readRSS(), g.readHeap()
	if heap > rss {
		return heap
	}
	return rss
}

// Ratio 当前占用比例（占用 / 限制）。
func (g *Governor) Ratio() float64 {
	return float64(g.used()) / float64(g.limit)
}

// TargetRatio 当前占用相对目标的比值（1.0 = 正好达标）。
func (g *Governor) TargetRatio() float64 {
	t := g.Target()
	if t <= 0 {
		return 0
	}
	return float64(g.used()) / float64(t)
}

// Tick 执行一次调节（Run 每 5 秒调用；测试可直接调用）。
func (g *Governor) Tick() {
	used := g.used()
	target := g.Target()
	ceil := g.ceiling()
	ratio := float64(used) / float64(g.limit)
	g.ratioBits.Store(math.Float64bits(ratio))
	prev := g.Level()
	next := prev

	switch {
	case used >= ceil:
		next = Critical
	case used >= target+target/10: // 超过目标 10% 起进入 warn（带一点回滞）
		if prev == Critical && used >= target {
			next = Critical
		} else {
			next = Warn
		}
	case used < target:
		next = Normal
	}

	if next != prev || !g.appliedOnce {
		g.appliedOnce = true
		g.level.Store(int32(next))
		log.Printf("内存压力: %s（占用 %d/%d MB，目标 %d MB，上限 %d MB）",
			next, used>>20, g.limit>>20, target>>20, ceil>>20)
		switch next {
		case Critical:
			// 触到上限：把软限压到目标以下，GOGC 15，并每 5 秒归还一次。
			g.apply(target*90/100, 15)
			g.maybeFreeOS(5 * time.Second)
		case Warn:
			// 超过目标：软限设为目标，GOGC 30。
			g.apply(target, 30)
		default:
			// 达标：软限留 10% 余量，避免目标附近频繁 GC。
			g.apply(target+target/10, 50)
		}
	}

	// 压力越大，归还越频繁（但都有下限，避免 GC/IO 抖动）。
	p := g.Pressure()
	switch {
	case p >= 0.8:
		g.maybeFreeOS(8 * time.Second)
	case p >= 0.5:
		g.maybeFreeOS(15 * time.Second)
	}

	// 空闲回收：30 秒没有请求、且堆超过目标的一半时归还（小目标下也积极归还）。
	if g.Level() == Normal {
		idle := time.Since(time.Unix(0, g.lastReq.Load())) > 30*time.Second
		if idle && g.readHeap() > target/2 {
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
