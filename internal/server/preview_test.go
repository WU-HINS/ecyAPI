package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	gwebp "github.com/gen2brain/webp"

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
	_, cachePath := srv.previewCachePath("pc", "big.jpg", 480)
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

	// 超出范围的宽度 404（过小 / 过大）。
	rec4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec4, httptest.NewRequest("GET", "/pic/pc/big.jpg?w=9999", nil))
	if rec4.Code != 404 {
		t.Fatalf("invalid width status = %d, want 404", rec4.Code)
	}
	rec4b := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec4b, httptest.NewRequest("GET", "/pic/pc/big.jpg?w=8", nil))
	if rec4b.Code != 404 {
		t.Fatalf("too-small width status = %d, want 404", rec4b.Code)
	}
}

// 自定义宽度（非 480/1280 档位）同样可压缩，且各宽度缓存互不干扰。
func TestPreviewCustomWidth(t *testing.T) {
	srv, catDir := newPreviewServer(t)
	big := makeBigJPEG(t, 2000, 1200)
	if err := os.WriteFile(filepath.Join(catDir, "big.jpg"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pic/pc/big.jpg?w=640", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	dec, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Bounds().Dx() != 640 {
		t.Fatalf("preview width = %d, want 640", dec.Bounds().Dx())
	}
}

// 队列满时不排队：无缓存则直接回退原图。
func TestPreviewQueueFullFallsBackToOriginal(t *testing.T) {
	srv, catDir := newPreviewServer(t)
	big := makeBigJPEG(t, 2000, 1200)
	if err := os.WriteFile(filepath.Join(catDir, "big.jpg"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	// 占满队列（容量 25）。
	for i := 0; i < previewQueueLen; i++ {
		previewQueue <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < previewQueueLen; i++ {
			<-previewQueue
		}
	})
	// 未命中缓存：应回退原图字节，而非 404 或挂起。
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pic/pc/big.jpg?w=320", nil))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queue-full request hung")
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), big) {
		t.Fatal("queue full without cache should fall back to original bytes")
	}
	// 已有缓存：队列满仍应命中缓存输出压缩版。
	_, cachePath := srv.previewCachePath("pc", "big.jpg", 320)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest("GET", "/pic/pc/big.jpg?w=320", nil))
	if rec2.Body.String() != "cached" {
		t.Fatal("queue full should still hit cache")
	}
}

// 删除图片后缓存应被同步清理，避免残留已删图片的缩略图。
func TestPreviewCacheCleanupOnDelete(t *testing.T) {
	srv, catDir := newPreviewServer(t)
	big := makeBigJPEG(t, 2000, 1200)
	orig := filepath.Join(catDir, "big.jpg")
	if err := os.WriteFile(orig, big, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pic/pc/big.jpg?w=480", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	_, cachePath := srv.previewCachePath("pc", "big.jpg", 480)
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	srv.cleanupPreviewCache("pc", "big.jpg")
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatal("cache should be removed after delete")
	}
}

// 预览输出应保持源图格式：webp 源出 webp，扩展名与 Content-Type 都不变。
func TestPreviewKeepsSourceFormatWebP(t *testing.T) {
	srv, catDir := newPreviewServer(t)
	// 用 x/image/webp 无法编码，借 gen2brain/webp 生成一张 webp 源图。
	src := image.NewRGBA(image.Rect(0, 0, 2000, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 2000; x++ {
			src.SetRGBA(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), uint8((x + y) % 256), 255})
		}
	}
	var buf bytes.Buffer
	if err := gwebp.Encode(&buf, src, gwebp.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	orig := buf.Bytes()
	if err := os.WriteFile(filepath.Join(catDir, "photo.webp"), orig, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pic/pc/photo.webp?w=480", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/webp" {
		t.Fatalf("content-type = %q, want image/webp", ct)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("RIFF")) {
		t.Fatal("preview body is not a webp (RIFF) file")
	}
	// 缓存文件扩展名也应为 .webp。
	_, cachePath := srv.previewCachePath("pc", "photo.webp", 480)
	if filepath.Ext(cachePath) != ".webp" {
		t.Fatalf("cache ext = %q, want .webp", filepath.Ext(cachePath))
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache not written: %v", err)
	}
}

// 预览输出应保持源图格式：png 源出 png。
func TestPreviewKeepsSourceFormatPNG(t *testing.T) {
	srv, catDir := newPreviewServer(t)
	src := image.NewRGBA(image.Rect(0, 0, 2000, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 2000; x++ {
			src.SetRGBA(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), uint8((x + y) % 256), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catDir, "img.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pic/pc/img.png?w=480", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q, want image/png", ct)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatal("preview body is not a png file")
	}
}

// jpeg 源保持 jpeg 输出（原有行为不变，防回归）。
func TestPreviewKeepsSourceFormatJPEG(t *testing.T) {
	srv, catDir := newPreviewServer(t)
	big := makeBigJPEG(t, 2000, 1200)
	if err := os.WriteFile(filepath.Join(catDir, "big.jpg"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pic/pc/big.jpg?w=480", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type = %q, want image/jpeg", ct)
	}
}
