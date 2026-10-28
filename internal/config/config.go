// Package config 加载服务配置：环境变量优先，缺省提供合理默认值。
package config

import (
	"os"
	"runtime/debug"
	"strconv"
)

// Config 服务全局配置。
type Config struct {
	Addr          string
	DataDir       string
	AdminPassword string
	MaxLinks      int
	BaseURL       string // 对外展示的 API 域名，如 https://api.example.com；留空则按请求推断
	// MaxUploadBytes 单次上传请求体上限（字节），超出直接拒绝。
	MaxUploadBytes int64
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
	}
}
