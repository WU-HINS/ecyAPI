package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
)

// newWParamServer 创建带一个 pc 栏目与一张 webp 图的测试服务。
func newWParamServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	dataDir := filepath.Join(dir, "categories")
	if err := os.MkdirAll(filepath.Join(dataDir, "pc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	// 一张最小 webp，内容不影响断言（透传测试只看链接）。
	// 先写文件再 New，让 store 初次扫描就包含图片。
	if err := os.WriteFile(filepath.Join(dataDir, "pc", "x.webp"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dataDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.addEndpoint("pc", "pc", "", "pc", ""); err != nil {
		t.Fatalf("addEndpoint: %v", err)
	}
	return s
}

// /pc?w=480 应 302 到带 ?w=480 的图片链接。
func TestLinkRedirectPassesWidthParam(t *testing.T) {
	s := newWParamServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pc?w=480", nil))
	if rec.Code != 302 {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasSuffix(loc, "/pic/pc/x.webp?w=480") {
		t.Fatalf("location = %q, want suffix /pic/pc/x.webp?w=480", loc)
	}
}

// 无 w 参数时 302 保持原链接不变。
func TestLinkRedirectWithoutWidthParam(t *testing.T) {
	s := newWParamServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pc", nil))
	loc := rec.Header().Get("Location")
	if !strings.HasSuffix(loc, "/pic/pc/x.webp") || strings.Contains(loc, "?") {
		t.Fatalf("location = %q, want clean /pic/pc/x.webp", loc)
	}
}

// /json?pc&w=480 返回的 link 应带 ?w=480。
func TestJSONPassesWidthParam(t *testing.T) {
	s := newWParamServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/json?pc&w=480", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `"link":"http://example.com/pic/pc/x.webp?w=480"`) &&
		!strings.Contains(body, `"link": "http://example.com/pic/pc/x.webp?w=480"`) {
		// httptest 请求 Host 为 example.com
		t.Fatalf("json body missing w param in link: %s", body)
	}
}

// /pc?json&quantity=2&w=320 纯文本输出也应带参数。
func TestLinkPlainTextPassesWidthParam(t *testing.T) {
	s := newWParamServer(t)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pc?json&w=320", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "?w=320") {
		t.Fatalf("plain text links missing w param: %q", body)
	}
}

// /json?分类&w=480：w 不得被误认成分类名（曾因 map 遍历顺序概率性 404）。
func TestJSONWParamNotMistakenAsCategory(t *testing.T) {
	for i := 0; i < 200; i++ {
		s := newWParamServer(t)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/json?pc&w=480", nil))
		body := rec.Body.String()
		if !strings.Contains(body, "pic/pc/x.webp") {
			t.Fatalf("iter %d: body = %s", i, body)
		}
	}
}
