// Package config 加载服务配置：环境变量优先，缺省提供合理默认值。
package config

import (
	"os"
	"runtime/debug"
	"strconv"

	"ecyapi/internal/store"
)

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
	// PreviewMP 缩略图压缩的总像素预算（ECY_PREVIEW_MP，默认 16 百万像素）。
	// 按「解码后位图大小」加权限制并发压缩数：4K 图约 8MP，预算 64 表示最多同时 8 张。
	PreviewMP int
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
	debug.SetGCPercent(50)
	debug.SetMemoryLimit(envInt64("ECY_MEMORY_LIMIT", 512<<20))

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
		GalleryMax:         clampInt(envInt("ECY_GALLERY_MAX", 0), 0, 5000),
		GalleryConcurrency: clampInt(envInt("ECY_GALLERY_CONCURRENCY", 2), 1, 16),
		GalleryWaitSeconds: clampInt(envInt("ECY_GALLERY_WAIT", 5), 1, 60),
		// 预览压缩预算：默认 32 百万像素（≈128MB RGBA），范围 8~512。
		// 4K 图约 8MP，即最多 4 张同时解码，其余回退原图，避免小内存容器 OOM。
		PreviewMP: clampInt(envInt("ECY_PREVIEW_MP", 16), 8, 512),
	}
}
