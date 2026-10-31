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

	// 最近一次实际应用的参数（日志用）
	appliedLim atomic.Int64
	appliedGCp atomic.Int32

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
	if g.hardPct < bandStartPct || g.hardPct > bandEndMax {
		g.hardPct = 0.85 // 区间终点默认 85%（70% 起算，逐步收紧）
	}
	if g.capPct <= 0 || g.capPct > 1 {
		g.capPct = 0.30
	}
	g.readRSS = readRSS
	g.readHeap = readHeap
	g.apply = func(limit int64, gcPercent int) {
		g.appliedLim.Store(limit)
		g.appliedGCp.Store(int32(gcPercent))
		debug.SetMemoryLimit(limit)
		debug.SetGCPercent(gcPercent)
	}
	g.freeOS = debug.FreeOSMemory
	g.NoteRequest() // 启动即视为活跃，避免刚启动就触发回收
	return g
}

// appliedLimit / appliedGC 最近一次实际应用的 GOMEMLIMIT 与 GOGC（日志用）。
func (g *Governor) appliedLimit() int64 { return g.appliedLim.Load() }
func (g *Governor) appliedGC() int      { return int(g.appliedGCp.Load()) }

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

// SetTargetLogged 设置目标并在「变化足够大」时打日志。
// 内存策略是被运维盯着的：目标从 25MB 放宽到 146MB 这种事必须留痕，
// 但每 3 秒重算一次不能每次都刷，所以只在变化超过 max(4MB, 5%) 时记录。
func (g *Governor) SetTargetLogged(bytes int64, reason string) {
	if bytes < 0 {
		bytes = 0
	}
	old := g.Target()
	if bytes > 0 && old > 0 {
		delta := bytes - old
		if delta < 0 {
			delta = -delta
		}
		min := int64(4 << 20)
		if old/20 > min {
			min = old / 20 // 5%
		}
		if delta < min {
			g.target.Store(bytes)
			return
		}
		log.Printf("内存目标调整：%dMB → %dMB（%s；上限 %dMB）",
			old>>20, bytes>>20, reason, g.targetCap()>>20)
	} else if bytes > 0 {
		log.Printf("内存目标：%dMB（%s；上限 %dMB）", bytes>>20, reason, g.targetCap()>>20)
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

// 降级区间：70% 起逐步收紧并发与后台策略，90% 到顶（系统再往上就开始 OOM）。
// 区间内不是二值切换，而是按进度 0→1 线性收缩。
const (
	bandStartPct = 0.70 // 降级区间起点
	bandEndMax   = 0.90 // 降级区间终点上限
	hardPctAbso  = 0.95 // 绝对上限：到这儿只能命中缓存
	hysteresis   = 0.05 // 回滞：掉到 红线-5% 才解除 critical
)

// ceiling 降级区间终点（critical 触发线），夹在 70%~90%。
func (g *Governor) ceiling() int64 {
	return int64(float64(g.limit) * g.bandEnd())
}

// bandEnd 降级区间终点比例。
func (g *Governor) bandEnd() float64 {
	p := g.hardPct
	if p < bandStartPct {
		p = bandStartPct
	}
	if p > bandEndMax {
		p = bandEndMax
	}
	return p
}

// absoluteMax 绝对上限（95%）：超过这里就不再允许任何大分配。
func (g *Governor) absoluteMax() int64 {
	return int64(float64(g.limit) * hardPctAbso)
}

// exitLine 回滞下限：占用回落到这里才解除 Critical（红线 -5%）。
func (g *Governor) exitLine() int64 {
	l := int64(float64(g.ceiling()) - float64(g.limit)*hysteresis)
	if l < 0 {
		return 0
	}
	return l
}

// TargetCap 自适应目标的最高值（对外暴露，启动日志用）。
func (g *Governor) TargetCap() int64 { return g.targetCap() }

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
	// 降级区间进度：70% 起为 0，区间终点（默认 80%~90%）为 1。
	// 用它线性收紧并发与后台策略（不是到点才降级）。
	ratio := g.Ratio()
	start := bandStartPct
	end := g.bandEnd()
	if end <= start {
		return 1
	}
	p := (ratio - start) / (end - start)
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
	// 准入看到 95% 绝对上限：目标是调节旋钮不是拒绝线（Go 自身常驻 ~20MB），
	// 但绝不允许把进程推到 90% 以上——那里系统会开始 OOM。
	return g.used()+need <= g.absoluteMax()
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

	// 降级区间 70%~90%：区间内是「连续降级」，档位只表示大致阶段。
	//   used ≥ 95%（绝对上限）   → critical（只能命中缓存）
	//   used ≥ 区间终点           → critical
	//   used ≥ 区间终点-5%        → 保持 critical（回滞，避免抖动）
	//   used ≥ 70%（区间内）      → warn（并发/后台已经开始收紧）
	//   used < 70%                → normal
	bandStart := int64(float64(g.limit) * bandStartPct)
	switch {
	case used >= g.absoluteMax():
		next = Critical
	case used >= ceil:
		next = Critical
	case prev == Critical && used >= g.exitLine():
		next = Critical
	case used >= bandStart:
		next = Warn
	default:
		next = Normal
	}
	if next == Normal && used >= target && target > 0 {
		next = Normal // 目标只影响 GC 强度，不改变档位
	}

	if next != prev || !g.appliedOnce {
		g.appliedOnce = true
		g.level.Store(int32(next))
		// 先落参数，日志里才能带上真实的 GOMEMLIMIT/GOGC。
		switch next {
		case Critical:
			// 区间终点/绝对上限：软限压到目标以下，GOGC 15，并每 5 秒归还一次。
			g.apply(target*90/100, 15)
			g.maybeFreeOS(5 * time.Second)
		case Warn:
			// 落在降级区间：软限设为目标，GOGC 30，按进度收紧并发与后台。
			g.apply(target, 30)
		default:
			// 达标：软限留 10% 余量，避免目标附近频繁 GC。
			g.apply(target+target/10, 50)
		}
		log.Printf("内存策略：%s｜占用 %dMB（目标 %dMB，降级区间 %d%%~%d%%，限额 %dMB，降级进度 %.0f%%）｜GOMEMLIMIT=%dMB GOGC=%d",
			next, used>>20, target>>20, int(bandStartPct*100), int(g.bandEnd()*100), g.limit>>20,
			g.Pressure()*100, g.appliedLimit()>>20, g.appliedGC())
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
