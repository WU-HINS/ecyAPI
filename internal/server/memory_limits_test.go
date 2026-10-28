package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ecyapi/internal/config"
)

// TestGallerySampling 图库按上限随机抽样，不再把整个分类搬进内存。
func TestGallerySampling(t *testing.T) {
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
	for i := 0; i < 240; i++ {
		name := filepath.Join(cat, "f"+strconv.Itoa(1000+i)+".png")
		if err := os.WriteFile(name, png, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(config.Config{DataDir: dataDir, AdminPassword: "pw", GalleryMax: 50})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/img", nil))
	body := rec.Body.String()
	if n := strings.Count(body, "class=\"image-card\""); n != 50 {
		t.Fatalf("图库渲染了 %d 张，期望 50 张（上限）", n)
	}
	total, err := s.st.Count(t.Context(), "big")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "共 "+strconv.Itoa(total)+" 张") {
		t.Fatalf("缺少总数提示（total=%d）", total)
	}
}

// TestPreviewGateBudget 像素预算闸门：超出预算直接拒绝，释放后恢复。
func TestPreviewGateBudget(t *testing.T) {
	g := newPreviewGate(8)
	if !g.tryAcquire(5) {
		t.Fatal("5/8 应允许")
	}
	if g.tryAcquire(4) {
		t.Fatal("5+4>8 应拒绝")
	}
	if !g.tryAcquire(3) {
		t.Fatal("5+3=8 应允许")
	}
	g.release(5)
	if !g.tryAcquire(5) {
		t.Fatal("释放后应恢复")
	}
	// 超过总预算的单张请求会被夹到预算上限
	g2 := newPreviewGate(4)
	if !g2.tryAcquire(100) {
		t.Fatal("单张超过预算应夹到上限并允许")
	}
	if g2.tryAcquire(1) {
		t.Fatal("已占满应拒绝")
	}
	cases := []struct{ w, h, want int }{
		{1, 1, 1}, {640, 480, 1}, {1920, 1080, 3}, {3840, 2160, 9},
	}
	for _, c := range cases {
		if got := previewUnits(c.w, c.h); got != c.want {
			t.Errorf("previewUnits(%d,%d)=%d，期望 %d", c.w, c.h, got, c.want)
		}
	}
}
