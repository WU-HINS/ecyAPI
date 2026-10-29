// Package config 加载服务配置：环境变量优先，缺省提供合理默认值。
package config

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"ecyapi/internal/store"
)

// isOff 解析 off/0/false/no 这类关闭开关（空值视为开启）。
func isOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// clampInt 把数值夹到 [lo, hi]。
func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// clampDepth 把 ECY_MAX_DEPTH 夹到 [1, store.MaxDepthLimit]。
func clampDepth(n int) int {
	if n < 1 {
		return store.DefaultMaxDepth
	}
	if n > store.MaxDepthLimit {
		return store.MaxDepthLimit
	}
	return n
}

// Config 服务全局配置。
type Config struct {
	Addr          string
	DataDir       string
	AdminPassword string
	MaxLinks      int
	BaseURL       string // 对外展示的 API 域名，如 https://api.example.com；留空则按请求推断
	// MaxUploadBytes 单次上传请求体上限（字节），超出直接拒绝。
	MaxUploadBytes int64
	// MaxDepth 分类层级上限（ECY_MAX_DEPTH，默认 3，上限 8）。
	MaxDepth int
	// GalleryMax 图库单次最多展示多少张（ECY_GALLERY_MAX，默认 0=全部）。
	// 设为 >0 时退化为「随机抽样 N 张」，用于极端大分类的兜底。
	GalleryMax int
	// GalleryConcurrency 图库页同时渲染的并发上限（ECY_GALLERY_CONCURRENCY，默认 2）。
	// 图库要展示全部图片，单次渲染开销随图片数增长，靠并发闸门把内存钉死。
	GalleryConcurrency int
	// GalleryWaitSeconds 图库排队等待上限（ECY_GALLERY_WAIT，默认 5 秒），超时返回 503。
	GalleryWaitSeconds int
	// ScanIntervalSeconds 后台增量刷新间隔（ECY_SCAN_INTERVAL，默认 3 秒）。
	// 扫描已移出请求路径，文件增删在这个间隔内生效。
	ScanIntervalSeconds int
	// StartupWaitSeconds 首次扫描的等待上限（ECY_STARTUP_WAIT，默认 15 秒）。
	// 超时则先启动服务、扫描在后台继续；0 表示等扫描完成再启动。
	StartupWaitSeconds int
	// VerifyIntervalSeconds 强制完整校验间隔（ECY_VERIFY_INTERVAL，默认 3600 秒 = 1 小时）。
	// 用于发现「同名文件被原地替换」（大小/mtime 变了才重建）；每次要 stat 全目录，默认给得比较宽。
	VerifyIntervalSeconds int
	// SnapshotTTLSeconds 磁盘快照 TTL（ECY_SNAPSHOT_TTL，默认 300 秒 = 5 分钟）。
	// 启动时有不超过该时长的快照就直接读回、跳过扫盘；运行期按同一间隔落盘。0 关闭。
	SnapshotTTLSeconds int
	// DisableLinkVerify 关闭 302 前的目标文件校验（ECY_VERIFY_LINKS=off）。
	// 默认开启：多一次热缓存 stat（约 1~5µs），换来「绝不 302 到已删除文件」。
	// 用「关闭」语义是为了让零值结构体默认就是安全行为。
	DisableLinkVerify bool
	// PreviewMP 缩略图压缩的总像素预算（ECY_PREVIEW_MP，默认 16 百万像素）。
	// 按「解码后位图大小」加权限制并发压缩数：4K 图约 8MP，预算 64 表示最多同时 8 张。
	PreviewMP int
	// PreviewCacheFiles 缩略图缓存文件数上限（ECY_PREVIEW_CACHE_FILES，默认 20000）。
	// 太小会导致「浏览图库时反复重压同一批图」，是持续预览下 CPU/内存抖动的来源。
	PreviewCacheFiles int
	// MemoryLimit 内存预算（字节）：容器 cgroup 限制，或显式 ECY_MEMORY_LIMIT。
	// 运行时由 memguard 按「软目标 75% / 红线 90%」动态调节 GOMEMLIMIT。
	MemoryLimit int64
	// MemSoftPct 软目标百分比（ECY_MEM_SOFT_PCT，默认 75）。
	MemSoftPct int
	// MemHardPct 红线百分比（ECY_MEM_HARD_PCT，默认 90）。
	MemHardPct int
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// Load 从环境变量加载配置。
func Load() Config {
	// Go 运行时默认按需保留堆内存（GOGC=100 才触发下一轮 GC，空闲页也
	// 不急于还给 OS），批量图片压缩后 RSS 常驻峰值不下落，容器监控里
	// 看起来像"内存泄漏"。GC 更早触发 + 软内存上限双管齐下，配合压缩
	// 队列排空时主动归还（见 preview.go），让 RSS 跟着实际用量回落。
	// 内存预算优先取环境变量，其次容器 cgroup 限制，最后兜底 512MB。
	// 初始 GOMEMLIMIT 取预算的 70%：Go 的软限是 GC 目标，设成预算值会让
	// RSS 紧贴容器上限；之后由 memguard 按压力在 55%~70% 之间动态调节。
	memLimit := effectiveMemoryLimit()
	debug.SetGCPercent(50)
	debug.SetMemoryLimit(memLimit * 70 / 100)

	return Config{
		Addr:          env("ECY_ADDR", ":8080"),
		DataDir:       env("ECY_DATA_DIR", "data/categories"),
		AdminPassword: env("ECY_ADMIN_PASSWORD", "ecy-admin"),
		MaxLinks:      envInt("ECY_MAX_LINKS", 50),
		BaseURL:       env("ECY_BASE_URL", ""),
		// 默认 512 MB：批量上传够用，又不至于让单个请求吃满磁盘。
		MaxUploadBytes: envInt64("ECY_MAX_UPLOAD_MB", 512) << 20,
		// 分类层级：默认 3 段（a/b/c），上限 8 段，超出按上限处理。
		MaxDepth: clampDepth(envInt("ECY_MAX_DEPTH", store.DefaultMaxDepth)),
		// 图库默认展示全部（0）；>0 时随机抽样，作为超大分类的兜底。
		GalleryMax:            clampInt(envInt("ECY_GALLERY_MAX", 0), 0, 5000),
		GalleryConcurrency:    clampInt(envInt("ECY_GALLERY_CONCURRENCY", 2), 1, 16),
		GalleryWaitSeconds:    clampInt(envInt("ECY_GALLERY_WAIT", 5), 1, 60),
		ScanIntervalSeconds:   clampInt(envInt("ECY_SCAN_INTERVAL", 3), 1, 3600),
		StartupWaitSeconds:    clampInt(envInt("ECY_STARTUP_WAIT", 15), 0, 300),
		VerifyIntervalSeconds: clampInt(envInt("ECY_VERIFY_INTERVAL", 3600), 5, 86400),
		SnapshotTTLSeconds:    clampInt(envInt("ECY_SNAPSHOT_TTL", 300), 0, 86400),
		DisableLinkVerify:     isOff(os.Getenv("ECY_VERIFY_LINKS")),
		// 预览压缩预算：未显式设置时按堆上限自适应（约 1/12，夹在 8~64MP），
		// 4K 图约 8MP：512MB 容器 → 32MP（2 张并发），192MB 容器 → 15MP（1 张）。
		PreviewMP: clampInt(envInt("ECY_PREVIEW_MP", previewBudgetMP(memLimit)), 8, 512),
		// 缩略图缓存条数：默认 2 万，避免浏览大图库时反复重压同一批图。
		PreviewCacheFiles: clampInt(envInt("ECY_PREVIEW_CACHE_FILES", 20000), 100, 1000000),
		MemoryLimit:       memLimit,
		MemSoftPct:        clampInt(envInt("ECY_MEM_SOFT_PCT", 75), 40, 90),
		MemHardPct:        clampInt(envInt("ECY_MEM_HARD_PCT", 90), 60, 99),
	}
}

// previewBudgetMP 依据堆上限推算并发的 RGBA 解码预算（百万像素）。
// 经验值：同时解码的位图总量不超过堆预算的 ~1/3（4 字节/像素，取 1/12 像素比）。
func previewBudgetMP(memLimit int64) int {
	mp := int(memLimit / (16 * 1_000_000))
	if mp < 8 {
		mp = 8
	}
	if mp > 64 {
		mp = 64
	}
	return mp
}

// effectiveMemoryLimit 返回内存预算：
//  1. 环境变量 ECY_MEMORY_LIMIT（显式指定，最高优先）；
//  2. 容器 cgroup 限制（v2 memory.max / v1 limit_in_bytes）；
//  3. 兜底 512MB。
func effectiveMemoryLimit() int64 {
	if v := os.Getenv("ECY_MEMORY_LIMIT"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	if n := cgroupMemoryLimit(); n > 0 {
		return n
	}
	return 512 << 20
}

// cgroupMemoryLimit 读取 cgroup v2 / v1 的内存上限；读不到或「无限制」返回 0。
func cgroupMemoryLimit() int64 {
	// cgroup v2
	if b, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		s := strings.TrimSpace(string(b))
		if s != "" && s != "max" {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
				return n
			}
		}
	}
	// cgroup v1（无限制时会是一个接近 2^63 的巨大值）
	if b, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil &&
			n > 0 && n < 1<<60 {
			return n
		}
	}
	return 0
}
