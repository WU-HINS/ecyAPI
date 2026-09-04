// ecyAPI — 栗次元随机图片 API 服务入口。
package main

import (
	"log"
	"net/http"

	"ecyapi/internal/config"
	"ecyapi/internal/store"
	"ecyapi/internal/server"
)

func main() {
	cfg := config.Load()

	dbDSN := ""
	if cfg.DB.Enabled() {
		dbDSN = store.DSN(cfg.DB.Host, cfg.DB.Port, cfg.DB.User, cfg.DB.Password, cfg.DB.Name, cfg.DB.Charset)
	}

	srv, err := server.New(cfg, dbDSN)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}

	log.Printf("ecyAPI 监听 %s（数据源: %s）", cfg.Addr, dataSource(cfg, dbDSN))
	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv.Handler(),
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}

func dataSource(cfg config.Config, dbDSN string) string {
	if dbDSN == "" {
		return "文件目录 " + cfg.DataDir
	}
	return "MySQL " + cfg.DB.Host
}
