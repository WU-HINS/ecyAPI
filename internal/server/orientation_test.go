package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
)

const (
	uaDesktop = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120 Safari/537.36"
	uaMobile  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148"
)

// newOrientServer 造一个混合方向的分类 mixed（1 横 1 竖），并登录。
func newOrientServer(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	dataDir := filepath.Join(dir, "categories")
	cat := filepath.Join(dataDir, "mixed")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatalf("mkdir cat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cat, "wide.png"), subdirPNG(t, 320, 180), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cat, "tall.png"), subdirPNG(t, 180, 320), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dataDir, AdminPassword: "pw"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest("POST", "/admin", strings.NewReader(url.Values{"login_password": {"pw"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "ecy_admin" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("未拿到会话 cookie")
	}
	return s, cookie
}

func getWithUA(s *Server, target, ua string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// TestAutoOrientationEndpoint 验证「自动横竖」栏目：桌面出横图、移动出竖图。
func TestAutoOrientationEndpoint(t *testing.T) {
	s, _ := newOrientServer(t)
	if err := s.addAdaptive("auto1", "mixed", "自动横竖", ""); err != nil {
		t.Fatalf("addAdaptive: %v", err)
	}
	for i := 0; i < 20; i++ { // 多次随机，确保筛选取的是横/竖桶而不是碰巧
		if rec := getWithUA(s, "/auto1", uaDesktop); rec.Code != http.StatusFound ||
			rec.Header().Get("Location") != "/pic/mixed/wide.png" {
			t.Fatalf("桌面端 = %d %q", rec.Code, rec.Header().Get("Location"))
		}
		if rec := getWithUA(s, "/auto1", uaMobile); rec.Code != http.StatusFound ||
			rec.Header().Get("Location") != "/pic/mixed/tall.png" {
			t.Fatalf("移动端 = %d %q", rec.Code, rec.Header().Get("Location"))
		}
	}
	// 纯文本与按序号两条出口同样生效
	if body := getWithUA(s, "/auto1?json", uaMobile).Body.String(); !strings.Contains(body, "tall.png") {
		t.Fatalf("移动端纯文本 = %q", body)
	}
	if body := getWithUA(s, "/auto1?json=1", uaDesktop).Body.String(); !strings.Contains(body, "wide.png") {
		t.Fatalf("桌面端按序号 = %q", body)
	}
	// 显式方向覆盖 UA
	if rec := getWithUA(s, "/auto1?orientation=portrait", uaDesktop); rec.Header().Get("Location") != "/pic/mixed/tall.png" {
		t.Fatalf("显式竖版覆盖失败: %q", rec.Header().Get("Location"))
	}
}

// TestAutoOrientationFallback 验证目标方向没有图时回退全部，不返回 404。
func TestAutoOrientationFallback(t *testing.T) {
	// 只有横图的分类必须在启动前就位（否则要等 3 秒节流重扫才会进缓存）。
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	dataDir := filepath.Join(dir, "categories")
	wideOnly := filepath.Join(dataDir, "wideonly")
	if err := os.MkdirAll(wideOnly, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wideOnly, "w.png"), subdirPNG(t, 320, 180), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dataDir, AdminPassword: "pw"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.addAdaptive("only", "wideonly", "只有横图", ""); err != nil {
		t.Fatalf("addAdaptive: %v", err)
	}
	rec := getWithUA(s, "/only", uaMobile) // 移动端要竖图 → 没有 → 回退横图
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/pic/wideonly/w.png" {
		t.Fatalf("回退失败: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestJSONOrientationParam 验证 /json 的方向参数与 auto 跟随 UA。
func TestJSONOrientationParam(t *testing.T) {
	s, _ := newOrientServer(t)
	cases := []struct {
		query, ua, want string
	}{
		{"?mixed&orientation=portrait", uaDesktop, "tall.png"},
		{"?mixed&orientation=landscape", uaMobile, "wide.png"},
		{"?mixed&o=竖", uaDesktop, "tall.png"},
		{"?mixed&orientation=auto", uaMobile, "tall.png"},
		{"?mixed&orientation=auto", uaDesktop, "wide.png"},
	}
	for _, c := range cases {
		rec := getWithUA(s, "/json"+c.query, c.ua)
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s 返回非 JSON: %q", c.query, rec.Body.String())
		}
		data, _ := payload["data"].(map[string]any)
		link, _ := data["link"].(string)
		if !strings.HasSuffix(link, "/pic/mixed/"+c.want) {
			t.Fatalf("%s (UA=%s) 得到 %q，期望 %s", c.query, c.ua, link, c.want)
		}
	}
}

// TestAdminBindingAuto 验证后台勾选「自动横竖」创建的栏目带 auto 标记。
func TestAdminBindingAuto(t *testing.T) {
	s, cookie := newOrientServer(t)
	form := url.Values{
		"binding_path":  {"zidong"},
		"binding_desk":  {"mixed"},
		"binding_label": {"自动"},
		"binding_auto":  {"1"},
	}
	req := httptest.NewRequest("POST", "/admin/binding", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "自动横竖") {
		t.Fatalf("后台提示 = %s", rec.Body.String())
	}
	s.mu.RLock()
	ep, ok := s.epIndex["zidong"]
	s.mu.RUnlock()
	if !ok || !ep.auto || ep.mobile != "" {
		t.Fatalf("栏目未按自动横竖创建: %+v (ok=%v)", ep, ok)
	}
}
