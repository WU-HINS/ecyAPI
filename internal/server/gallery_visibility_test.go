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

// newGalleryTestServer 创建两个分类（pc / secret）的测试服务并登录。
func newGalleryTestServer(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	for _, cat := range []string{"pc", "secret"} {
		if err := os.MkdirAll(filepath.Join(dir, "categories", cat), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "categories", cat, "a.png"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	s, err := New(config.Config{DataDir: filepath.Join(dir, "categories"), AdminPassword: "pw"})
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
		t.Fatal("no session cookie")
	}
	return s, cookie
}

func getGallery(s *Server, query string) string {
	req := httptest.NewRequest("GET", "/img"+query, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}

// postVisibility 提交图库可见性切换表单。
func postVisibility(t *testing.T, s *Server, cookie *http.Cookie, category string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/gallery/visibility",
		strings.NewReader(url.Values{"category": {category}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle %s: status %d, body %s", category, rec.Code, rec.Body.String())
	}
}

// 隐藏的分类不出现在图库；直接访问该分类时回退到第一个可见分类；状态可恢复。
func TestGalleryVisibilityToggle(t *testing.T) {
	s, cookie := newGalleryTestServer(t)

	if body := getGallery(s, ""); !strings.Contains(body, "secret") {
		t.Fatalf("初始应展示 secret 分类: %s", body)
	}

	postVisibility(t, s, cookie, "secret")

	if body := getGallery(s, ""); strings.Contains(body, "?category=secret") {
		t.Fatalf("隐藏后图库不应出现 secret 入口")
	} else if !strings.Contains(body, "pc") {
		t.Fatal("pc 分类应仍展示")
	}
	if body := getGallery(s, "?category=secret"); !strings.Contains(body, "pc 图片集") {
		t.Fatalf("访问隐藏分类应回退到 pc: %s", body)
	}

	// 持久化到 gallery.json
	data, err := os.ReadFile("data/gallery.json")
	if err != nil {
		t.Fatalf("read gallery.json: %v", err)
	}
	var hidden []string
	if err := json.Unmarshal(data, &hidden); err != nil || len(hidden) != 1 || hidden[0] != "secret" {
		t.Fatalf("gallery.json 内容不对: %s", data)
	}

	postVisibility(t, s, cookie, "secret")
	if body := getGallery(s, ""); !strings.Contains(body, "secret") {
		t.Fatal("恢复后应重新展示 secret")
	}
}

// 隐藏状态在重启后应恢复。
func TestGalleryVisibilityPersisted(t *testing.T) {
	s, cookie := newGalleryTestServer(t)
	postVisibility(t, s, cookie, "secret")

	s2, err := New(config.Config{DataDir: s.dataDir, AdminPassword: "pw"})
	if err != nil {
		t.Fatalf("reload New: %v", err)
	}
	if body := getGallery(s2, ""); strings.Contains(body, "?category=secret") {
		t.Fatal("重启后 secret 应保持隐藏")
	}
}

// 未登录不可切换。
func TestGalleryVisibilityUnauthorized(t *testing.T) {
	s, _ := newGalleryTestServer(t)
	req := httptest.NewRequest("POST", "/admin/gallery/visibility",
		strings.NewReader(url.Values{"category": {"secret"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，得到 %d", rec.Code)
	}
}

// 后台管理页应标注各分类的图库状态。
func TestAdminShowsGalleryState(t *testing.T) {
	s, cookie := newGalleryTestServer(t)
	postVisibility(t, s, cookie, "secret")

	req := httptest.NewRequest("GET", "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "已隐藏") || !strings.Contains(body, "展示中") {
		t.Fatalf("后台应展示图库状态: %s", body)
	}
}
