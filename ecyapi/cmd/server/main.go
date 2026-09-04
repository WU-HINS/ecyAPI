// ecyAPI — 栗次元随机图片 API 服务入口。
package main

import (
	"log"
	"net/http"

	"ecyapi/internal/config"
	"ecyapi/internal/server"
)

func main() {
	cfg := config.Load()

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}

	log.Printf("ecyAPI 监听 %s（数据源: 本地目录 %s）", cfg.Addr, cfg.DataDir)
	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv.Handler(),
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}
