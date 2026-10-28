package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
)

// escapedPath 把可能含中文/空格的分类路径按段转义成合法 URL 路径。
func escapedPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/" + strings.Join(segs, "/")
}

// newCategoryURLServer 造出磁盘目录但不建任何栏目，验证「目录即接口」。
func newCategoryURLServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "categories")
	write := func(rel string, w, h int) {
		p := filepath.Join(dataDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, subdirPNG(t, w, h), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pixiv/KuroiDa/wide.png", 320, 180)
	write("pixiv/KuroiDa/tall.png", 180, 320)
	write("2024-01/a.png", 200, 120)
	write("中文 目录/b.png", 200, 120)
	s, err := New(config.Config{DataDir: dataDir, AdminPassword: "pw"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// TestCategoryURLWithoutEndpoint 没有后台栏目的分类，URL 也直接可用。
func TestCategoryURLWithoutEndpoint(t *testing.T) {
	s := newCategoryURLServer(t)
	get := func(p, ua string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", p, nil)
		if ua != "" {
			req.Header.Set("User-Agent", ua)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}

	// 1) 嵌套分类，桌面端取横图
	rec := get(escapedPath("pixiv/KuroiDa"), uaDesktop)
	if rec.Code != http.StatusFound || !strings.HasSuffix(rec.Header().Get("Location"), "/pic/pixiv/KuroiDa/wide.png") {
		t.Fatalf("嵌套分类跳转 = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// 2) 同一路径，移动端取竖图（自动横竖）
	rec = get(escapedPath("pixiv/KuroiDa"), uaMobile)
	if rec.Code != http.StatusFound || !strings.HasSuffix(rec.Header().Get("Location"), "/pic/pixiv/KuroiDa/tall.png") {
		t.Fatalf("移动端跳转 = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// 3) ?json 纯文本
	if body := get(escapedPath("pixiv/KuroiDa")+"?json", uaDesktop).Body.String(); !strings.Contains(body, "/pic/pixiv/KuroiDa/") {
		t.Fatalf("纯文本 = %q", body)
	}
	// 4) 带横线/数字开头的目录名
	rec = get(escapedPath("2024-01"), uaDesktop)
	if rec.Code != http.StatusFound || !strings.HasSuffix(rec.Header().Get("Location"), "/pic/2024-01/a.png") {
		t.Fatalf("2024-01 跳转 = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// 5) 中文 + 空格目录
	rec = get(escapedPath("中文 目录"), uaDesktop)
	if rec.Code != http.StatusFound {
		t.Fatalf("中文目录跳转 = %d", rec.Code)
	}
	// 6) 图片本身可访问
	if rec := get("/pic"+escapedPath("中文 目录")+"/b.png", ""); rec.Code != http.StatusOK {
		t.Fatalf("中文目录图片 = %d", rec.Code)
	}
	// 6.5) 父目录（自身无图、只有子目录）聚合子分类出图
	rec = get(escapedPath("pixiv"), uaDesktop)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "/pic/pixiv/KuroiDa/") {
		t.Fatalf("父目录聚合跳转 = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// 7) 不存在的路径仍然是 404
	if rec := get("/no-such-thing", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("不存在路径 = %d", rec.Code)
	}
	// 8) 保留路径不受影响
	if rec := get("/json?pixiv/KuroiDa", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "code") {
		t.Fatalf("保留路径 /json = %d %q", rec.Code, rec.Body.String())
	}
}

// TestEndpointOverridesCategory 显式栏目优先于同名目录回退。
func TestEndpointOverridesCategory(t *testing.T) {
	s := newCategoryURLServer(t)
	if err := s.addEndpoint("pixiv/KuroiDa", "2024-01", "", "别名", ""); err != nil {
		t.Fatalf("addEndpoint: %v", err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", escapedPath("pixiv/KuroiDa"), nil))
	if rec.Code != http.StatusFound || !strings.HasSuffix(rec.Header().Get("Location"), "/pic/2024-01/a.png") {
		t.Fatalf("显式栏目未生效: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
