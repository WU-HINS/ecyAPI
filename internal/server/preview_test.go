package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ecyapi/internal/config"
)

// 生成一张 2000x1200 的测试大图（渐变噪声，难以被简单编码压小）。
func makeBigJPEG(t *testing.T, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8((x*7 + y*3) % 256),
				G: uint8((x*5 + y*11) % 256),
				B: uint8((x*13 + y*2) % 256),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newPreviewServer(t *testing.T) (*Server, string) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "categories")
	if err := os.MkdirAll(filepath.Join(dataDir, "pc"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(oldWd) })
	srv, err := New(config.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	return srv, filepath.Join(dataDir, "pc")
}

func TestPreviewCompressesAndCaches(t *testing.T) {
	srv, catDir := newPreviewServer(t)
	big := makeBigJPEG(t, 2000, 1200)
	orig := filepath.Join(catDir, "big.jpg")
	if err := os.WriteFile(orig, big, 0o644); err != nil {
		t.Fatal(err)
	}

	// 一次完整请求：应 200、image/jpeg、明显小于原图。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pic/pc/big.jpg?w=480", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.Bytes()
	if len(body) >= len(big) {
		t.Fatalf("preview %d bytes >= original %d bytes", len(body), len(big))
	}
	// 2000px 宽的图缩到 480 后行数约 288：解码头验证尺寸。
	dec, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if dec.Bounds().Dx() != 480 {
		t.Fatalf("preview width = %d, want 480", dec.Bounds().Dx())
	}

	// 缓存文件应已落盘。
	_, cachePath := srv.previewCachePath(orig, "big.jpg", 480)
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache not written: %v", err)
	}

	// 二次请求命中缓存，内容一致。
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest("GET", "/pic/pc/big.jpg?w=480", nil))
	if !bytes.Equal(rec2.Body.Bytes(), body) {
		t.Fatal("second request should hit identical cache")
	}

	// 无 w 参数仍是原图直出。
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, httptest.NewRequest("GET", "/pic/pc/big.jpg", nil))
	if !bytes.Equal(rec3.Body.Bytes(), big) {
		t.Fatal("request without w should return original bytes")
	}

	// 非法宽度 404。
	rec4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec4, httptest.NewRequest("GET", "/pic/pc/big.jpg?w=9999", nil))
	if rec4.Code != 404 {
		t.Fatalf("invalid width status = %d, want 404", rec4.Code)
	}
}
