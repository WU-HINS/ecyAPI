package server

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ecyapi/internal/config"
)

// setupBenchServer 建立一个 200 张图的分类，用于压 handler 热路径。
func setupBenchServer(b *testing.B) *Server {
	b.Helper()
	b.Setenv("ECY_ACCESS_LOG", "off") // 基准只测请求路径，不测日志 IO
	dir := b.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.MkdirAll("data", 0o755); err != nil {
		b.Fatal(err)
	}
	dataDir := filepath.Join(dir, "categories")
	cat := filepath.Join(dataDir, "pc")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		b.Fatal(err)
	}
	png := subdirPNG(&testing.T{}, 64, 36) // 小图，避免基准被文件读盘主导
	for i := 0; i < 200; i++ {
		if err := os.WriteFile(filepath.Join(cat, fmt.Sprintf("f%03d.png", i)), png, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	s, err := New(config.Config{DataDir: dataDir, AdminPassword: "pw", MaxLinks: 50})
	if err != nil {
		b.Fatal(err)
	}
	return s
}

// BenchmarkJSONSingle 单条 JSON 接口（最热路径）。
func BenchmarkJSONSingle(b *testing.B) {
	s := setupBenchServer(b)
	h := s.Handler()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/json?pc", nil))
	}
}

// BenchmarkJSONParallel 并发下的单条 JSON。
func BenchmarkJSONParallel(b *testing.B) {
	s := setupBenchServer(b)
	h := s.Handler()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/json?pc", nil))
		}
	})
}

// BenchmarkPic 原图输出。
func BenchmarkPic(b *testing.B) {
	s := setupBenchServer(b)
	h := s.Handler()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/pic/pc/f000.png", nil))
	}
}

// BenchmarkPreviewCacheHit 缩略图缓存命中。
func BenchmarkPreviewCacheHit(b *testing.B) {
	s := setupBenchServer(b)
	h := s.Handler()
	// 预热缓存
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/pic/pc/f000.png?w=64", nil))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/pic/pc/f000.png?w=64", nil))
	}
}
