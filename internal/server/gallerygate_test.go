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
	// 测试进程（尤其 -race）自身 RSS 会被算进内存压力，这里给一个大限额，
	// 让压力≈0，避免「预热因内存压力让路」这类与用例无关的抖动。
	cfg.MemoryLimit = 4 << 30
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

// TestGalleryHTMLCache 图库渲染结果落盘缓存；索引代次变化后换新缓存。
func TestGalleryHTMLCache(t *testing.T) {
	s := newGalleryServer(t, 50, nil)

	rec1 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec1, httptest.NewRequest("GET", "/img", nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("首次渲染状态码 %d", rec1.Code)
	}

	matches, _ := filepath.Glob(filepath.Join(s.galleryCacheRoot(), "*", "big.html"))
	if len(matches) != 1 {
		t.Fatalf("应生成 1 个图库缓存文件，实际 %d", len(matches))
	}

	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, httptest.NewRequest("GET", "/img", nil))
	if rec2.Body.String() != rec1.Body.String() {
		t.Fatalf("缓存命中内容与首次渲染不一致")
	}
	if ct := rec2.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("缓存命中 Content-Type = %q", ct)
	}

	// 新增文件 → 经写路径触发重扫 → 代次变化 → 旧缓存目录被清理
	before := s.st.Generation()
	png := subdirPNG(t, 16, 9)
	if err := os.WriteFile(filepath.Join(s.dataDir, "big", "zzz.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.admin.CreateCategory(t.Context(), "tmpcat"); err != nil {
		t.Fatal(err)
	}
	if s.st.Generation() == before {
		t.Fatalf("扫描后索引代次未变化")
	}
	rec3 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec3, httptest.NewRequest("GET", "/img", nil))
	matches, _ = filepath.Glob(filepath.Join(s.galleryCacheRoot(), "*", "big.html"))
	if len(matches) != 1 {
		t.Fatalf("旧代次缓存应被清理，实际 %d 个", len(matches))
	}
}
