package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPreviewInvalidatedOnReplace 源图被同名替换后，缩略图缓存必须失效重压。
func TestPreviewInvalidatedOnReplace(t *testing.T) {
	srv, catDir := newPreviewServer(t)
	src := filepath.Join(catDir, "swap.jpg")
	if err := os.WriteFile(src, makeBigJPEG(t, 800, 600), 0o644); err != nil {
		t.Fatal(err)
	}

	get := func() []byte {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pic/pc/swap.jpg?w=480", nil))
		if rec.Code != 200 {
			t.Fatalf("状态码 %d", rec.Code)
		}
		return rec.Body.Bytes()
	}

	first := get()
	_, cachePath := srv.previewCachePath("pc", "swap.jpg", 480)
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("首次请求应生成缓存: %v", err)
	}

	// 同名替换成另一张图，并把 mtime 推到缓存之后
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(src, makeBigJPEG(t, 400, 800), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(src, future, future); err != nil {
		t.Fatal(err)
	}

	second := get()
	if string(second) == string(first) {
		t.Fatal("源图替换后仍返回旧缩略图（缓存未失效）")
	}
	// 缓存文件应被重写（mtime 晚于源文件）
	fi, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.ModTime().Before(future) {
		t.Fatalf("缓存文件未被重写: cache=%v src=%v", fi.ModTime(), future)
	}
}
