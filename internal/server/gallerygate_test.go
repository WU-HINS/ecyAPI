package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ecyapi/internal/config"
)

// newGalleryServer 建一个含 n 张图的分类并返回服务。
func newGalleryServer(t *testing.T, n int, mutate func(*config.Config)) *Server {
	t.Helper()
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "categories")
	cat := filepath.Join(dataDir, "big")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatal(err)
	}
	png := subdirPNG(t, 16, 9)
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(cat, "f"+strconv.Itoa(1000+i)+".png"), png, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{DataDir: dataDir, AdminPassword: "pw", GalleryConcurrency: 2, GalleryWaitSeconds: 1}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestGalleryShowsAllByDefault 默认展示全部（不抽样）。
func TestGalleryShowsAllByDefault(t *testing.T) {
	s := newGalleryServer(t, 240, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/img", nil))
	body := rec.Body.String()
	if n := strings.Count(body, "class=\"image-card\""); n != 240 {
		t.Fatalf("默认应展示全部 240 张，实际 %d", n)
	}
	if strings.Contains(body, "随机展示") {
		t.Fatalf("默认不应出现抽样提示")
	}
}

// TestGallerySamplingWhenCapped 显式设置上限时才抽样（兜底）。
func TestGallerySamplingWhenCapped(t *testing.T) {
	s := newGalleryServer(t, 240, func(c *config.Config) { c.GalleryMax = 50 })
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/img", nil))
	body := rec.Body.String()
	if n := strings.Count(body, "class=\"image-card\""); n != 50 {
		t.Fatalf("上限 50 时应展示 50 张，实际 %d", n)
	}
	if !strings.Contains(body, "共 240 张") {
		t.Fatalf("应提示总数")
	}
}

// TestGalleryQueueFullReturns503 并发闸门饱和且排队超时 → 503 + Retry-After。
func TestGalleryQueueFullReturns503(t *testing.T) {
	s := newGalleryServer(t, 10, func(c *config.Config) {
		c.GalleryConcurrency = 1
		c.GalleryWaitSeconds = 1
	})
	// 占满唯一的渲染名额
	if !s.galleryGate.acquire(context.Background()) {
		t.Fatal("首次获取应成功")
	}
	start := time.Now()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/img", nil))
	elapsed := time.Since(start)
	s.galleryGate.release()

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("名额占满时排队超时应 503，得到 %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatalf("503 应带 Retry-After")
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("应等待约 1 秒后超时，实际 %v", elapsed)
	}
	// 释放后应恢复
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/img", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("释放后应恢复，得到 %d", rec.Code)
	}
}
