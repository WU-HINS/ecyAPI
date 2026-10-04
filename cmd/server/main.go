// ecyAPI — 栗次元随机图片 API 服务入口。
package main

import (
	"log"
	"net/http"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"ecyapi/internal/config"
	"ecyapi/internal/server"
)

func main() {
	cfg := config.Load()

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}

	log.Printf("ecyAPI 监听 %s（数据源: 本地目录 %s，支持 HTTP/1.1 与 h2c）", cfg.Addr, cfg.DataDir)
	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: h2c.NewHandler(srv.Handler(), &http2.Server{}),
		// 万人并发下必须设置超时，防慢客户端耗尽连接。
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // 大图/视频传输放宽
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}
