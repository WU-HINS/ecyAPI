// ecyAPI — 栗次元随机图片 API 服务入口。
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"ecyapi/internal/config"
	"ecyapi/internal/petassets"
	"ecyapi/internal/server"
)

func main() {
	// 内置的素材下载器：Docker 等没有 Go 工具链的环境里补看板娘动画最方便。
	//   docker exec <容器> /app/ecyapi -fetch-assets
	fetchAssets := flag.Bool("fetch-assets", false, "下载看板娘素材到 ECY_STATIC_DIR 后退出")
	flag.Parse()

	cfg := config.Load()
	if *fetchAssets {
		log.Printf("开始下载看板娘素材到 %s …", cfg.StaticDir)
		if _, err := petassets.Fetch(cfg.StaticDir, petassets.DefaultBase, log.Printf); err != nil {
			log.Fatalf("下载失败: %v", err)
		}
		return
	}

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
	// 优雅关闭：收到 SIGTERM/SIGINT 后停止接收新连接，等处理中的请求收尾。
	// 容器滚动发布时避免正在上传的图片被硬切断。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		log.Printf("收到 %s，开始优雅关闭…", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("优雅关闭超时，强制退出: %v", err)
		}
	}()

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务退出: %v", err)
	}
	log.Print("服务已停止")
}
