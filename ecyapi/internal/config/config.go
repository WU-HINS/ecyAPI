// Package config 加载服务配置：环境变量优先，缺省提供合理默认值。
package config

import (
	"os"
	"strconv"
)

// Config 服务全局配置。
type Config struct {
	Addr          string
	DataDir       string
	AdminPassword string
	MaxLinks      int
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

// Load 从环境变量加载配置。
func Load() Config {
	return Config{
		Addr:          env("ECY_ADDR", ":8080"),
		DataDir:       env("ECY_DATA_DIR", "data/categories"),
		AdminPassword: env("ECY_ADMIN_PASSWORD", "ecy-admin"),
		MaxLinks:      envInt("ECY_MAX_LINKS", 50),
	}
}
