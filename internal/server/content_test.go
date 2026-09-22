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

// newContentTestServer 创建带登录态的测试服务（数据目录与 data 目录均在临时目录内）。
func newContentTestServer(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	s, err := New(config.Config{DataDir: filepath.Join(dir, "categories"), AdminPassword: "pw"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
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
		t.Fatal("no session cookie")
	}
	return s, cookie
}

// postContent 提交首页文案表单。
func postContent(t *testing.T, s *Server, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/content", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// getHome 抓取首页 HTML。
func getHome(t *testing.T, s *Server) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}

// 首页不应再出现默认的竖杠分隔符，且备案号链接可自定义。
func TestHomeICPBarRemovedAndLinkCustomizable(t *testing.T) {
	s, cookie := newContentTestServer(t)

	home := getHome(t, s)
	if strings.Contains(home, "| <a") || strings.Contains(home, "<p>|") {
		t.Fatalf("home should not render the leading vertical bar, got:\n%s", home)
	}
	if !strings.Contains(home, defaultICPURL) {
		t.Fatalf("default icp url missing in home: %s", home)
	}

	// 自定义链接
	postContent(t, s, cookie, url.Values{
		"icp":     {"备案号自定义"},
		"icp_url": {"example.com/icp?k=1"},
	})
	home = getHome(t, s)
	if !strings.Contains(home, `href="https://example.com/icp?k=1"`) {
		t.Fatalf("custom icp url not rendered: %s", home)
	}
	if !strings.Contains(home, "备案号自定义") {
		t.Fatalf("custom icp text not rendered: %s", home)
	}

	// 链接留空 + 自定义文字 => 不生成超链接，仅纯文本
	postContent(t, s, cookie, url.Values{
		"icp":     {"无链接备案号"},
		"icp_url": {""},
	})
	home = getHome(t, s)
	if strings.Contains(home, defaultICPURL) {
		t.Fatalf("icp url should be cleared, but default link still present: %s", home)
	}
	if !strings.Contains(home, "<b>无链接备案号</b>") {
		t.Fatalf("plain icp text missing: %s", home)
	}
}

// 自定义 ICP 链接应持久化到 settings.json，并在重启后恢复。
func TestICPSettingsPersisted(t *testing.T) {
	s, cookie := newContentTestServer(t)
	postContent(t, s, cookie, url.Values{
		"icp":     {"萌ICP备999号"},
		"icp_url": {"https://example.org/beian"},
	})

	data, err := os.ReadFile("data/settings.json")
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(data), `"icp_url": "https://example.org/beian"`) {
		t.Fatalf("settings.json missing icp_url: %s", data)
	}

	s2, err := New(config.Config{DataDir: s.dataDir, AdminPassword: "pw"})
	if err != nil {
		t.Fatalf("reload New: %v", err)
	}
	if s2.icpURL != "https://example.org/beian" || s2.icp != "萌ICP备999号" {
		t.Fatalf("settings not restored: icp=%q icpURL=%q", s2.icp, s2.icpURL)
	}
}
