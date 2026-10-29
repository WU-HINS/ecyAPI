package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPreviewBackgroundFill 预算占满时：请求立刻回退原图，同时后台把缓存补上。
func TestPreviewBackgroundFill(t *testing.T) {
	srv, catDir := newPreviewServer(t)
	big := makeBigJPEG(t, 800, 600)
	if err := os.WriteFile(filepath.Join(catDir, "bg.jpg"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(srv.previewCacheRoot(), "pc", "bg.jpg.w480.webp")

	// 占满像素预算，模拟「图库持续预览、预算一直紧张」
	if !srv.previewGate.tryAcquire(srv.previewGate.cap) {
		t.Fatal("占满预算失败")
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pic/pc/bg.jpg?w=480", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("预算占满时应回退原图，状态码 %d", rec.Code)
	}
	if _, err := os.Stat(cache); err == nil {
		t.Fatal("预算占满时不应内联压缩写缓存")
	}

	// 释放预算后，后台 worker 应在几秒内补上缓存
	srv.previewGate.release(srv.previewGate.cap)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(cache); err == nil && fi.Size() > 0 {
			// 再次请求应命中缓存（image/webp）
			rec = httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pic/pc/bg.jpg?w=480", nil))
			if ct := rec.Header().Get("Content-Type"); ct != "image/webp" {
				t.Fatalf("命中缓存时 Content-Type = %q", ct)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("后台补缓存未生效")
}
