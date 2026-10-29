package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
)

// newAllRandomServer 造一个含三分类、每类一横一竖的实例。
func newAllRandomServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "categories")
	for _, cat := range []string{"alpha", "beta", "gamma"} {
		p := filepath.Join(dataDir, cat)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "wide.png"), subdirPNG(t, 40, 20), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "tall.png"), subdirPNG(t, 20, 40), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(config.Config{DataDir: dataDir, AdminPassword: "pw", PreviewMP: 16})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestAllRandomCoversAllCategories /@img 应能抽到所有已注册分类。
func TestAllRandomCoversAllCategories(t *testing.T) {
	s := newAllRandomServer(t)
	seen := map[string]int{}
	for i := 0; i < 90; i++ {
		req := httptest.NewRequest("GET", "/@img", nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64)")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("应 302，得到 %d", rec.Code)
		}
		loc := rec.Header().Get("Location")
		if !strings.HasPrefix(loc, "/pic/") {
			t.Fatalf("跳转地址不对: %s", loc)
		}
		seen[strings.Split(strings.TrimPrefix(loc, "/pic/"), "/")[0]]++
	}
	for _, cat := range []string{"alpha", "beta", "gamma"} {
		if seen[cat] == 0 {
			t.Fatalf("分类 %s 一次都没抽到（90 次）：%v", cat, seen)
		}
	}
}

// TestAllRandomAutoOrientation 桌面取横图、移动端取竖图。
func TestAllRandomAutoOrientation(t *testing.T) {
	s := newAllRandomServer(t)
	cases := []struct{ ua, want string }{
		{"Mozilla/5.0 (X11; Linux x86_64)", "wide.png"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148", "tall.png"},
	}
	for _, c := range cases {
		for i := 0; i < 12; i++ {
			req := httptest.NewRequest("GET", "/@img", nil)
			req.Header.Set("User-Agent", c.ua)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusFound {
				t.Fatalf("%s: 状态码 %d", c.ua, rec.Code)
			}
			if loc := rec.Header().Get("Location"); !strings.HasSuffix(loc, "/"+c.want) {
				t.Fatalf("%s: 应取 %s，实际 %s", c.ua, c.want, loc)
			}
		}
	}
}

// TestAllRandomJSONAndWidth ?json 输出纯文本，?w= 透传。
func TestAllRandomJSONAndWidth(t *testing.T) {
	s := newAllRandomServer(t)

	req := httptest.NewRequest("GET", "/@img?json", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/pic/") {
		t.Fatalf("?json 应返回链接文本，得到 %d %q", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest("GET", "/@img?w=480", nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "?w=480") {
		t.Fatalf("?w= 未透传: %s", loc)
	}
}

// TestAllRandomEmpty 空数据源应 404。
func TestAllRandomEmpty(t *testing.T) {
	dir := t.TempDir()
	s, err := New(config.Config{DataDir: filepath.Join(dir, "categories"), AdminPassword: "pw", PreviewMP: 16})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/@img", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("空数据源应 404，得到 %d", rec.Code)
	}
}

// TestAllRandomSkipsHidden 图库隐藏的分类不参与 /@img。
func TestAllRandomSkipsHidden(t *testing.T) {
	s := newAllRandomServer(t)
	s.mu.Lock()
	s.galleryHidden = []string{"beta"}
	s.mu.Unlock()

	seen := map[string]int{}
	for i := 0; i < 80; i++ {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/@img", nil))
		if rec.Code != http.StatusFound {
			t.Fatalf("应 302，得到 %d", rec.Code)
		}
		loc := strings.TrimPrefix(rec.Header().Get("Location"), "/pic/")
		seen[strings.Split(loc, "/")[0]]++
	}
	if seen["beta"] != 0 {
		t.Fatalf("隐藏分类 beta 不应被抽到：%v", seen)
	}
	if seen["alpha"] == 0 || seen["gamma"] == 0 {
		t.Fatalf("未隐藏的分类应能抽到：%v", seen)
	}
}
