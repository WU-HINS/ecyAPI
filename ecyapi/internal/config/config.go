// Package config 加载服务配置：环境变量优先，缺省提供合理默认值。
package config

import (
	"os"
	"strconv"
)

// DBConfig MySQL 连接配置（零值表示未启用数据库）。
type DBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	Charset  string
}

// Enabled 报告数据库是否配置完整。
func (d DBConfig) Enabled() bool {
	return d.Host != "" && d.User != "" && d.Name != ""
}

// Config 服务全局配置。
type Config struct {
	Addr          string
	DataDir       string
	AdminPassword string
	MaxLinks      int
	DB            DBConfig
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
	port := envInt("ECY_DB_PORT", 3306)
	if port < 1 || port > 65535 {
		port = 3306
	}
	return Config{
		Addr:          env("ECY_ADDR", ":8080"),
		DataDir:       env("ECY_DATA_DIR", "data/categories"),
		AdminPassword: env("ECY_ADMIN_PASSWORD", "ecy-admin"),
		MaxLinks:      envInt("ECY_MAX_LINKS", 50),
		DB: DBConfig{
			Host:     env("ECY_DB_HOST", ""),
			Port:     port,
			User:     env("ECY_DB_USER", ""),
			Password: env("ECY_DB_PASSWORD", ""),
			Name:     env("ECY_DB_NAME", ""),
			Charset:  env("ECY_DB_CHARSET", "utf8mb4"),
		},
	}
}
