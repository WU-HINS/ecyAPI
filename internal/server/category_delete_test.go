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

// newDeleteTestServer 创建带登录态的测试服务，返回服务与登录 cookie。
func newDeleteTestServer(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	dir := t.TempDir()
	// endpointsFile 常量是相对路径 "data/endpoints.json"，测试进程切到临时目录。
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	s, err := New(config.Config{DataDir: filepath.Join(dir, "categories"), AdminPassword: "pw"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// saveEndpoints 写的是相对路径 data/endpoints.json，先建好目录。
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	// 登录拿会话
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
		t.Fatalf("no session cookie")
	}
	return s, cookie
}

// TestCategoryDeleteCascade 验证删除分类会删除目录图片并级联下线关联栏目。
func TestCategoryDeleteCascade(t *testing.T) {
	s, cookie := newDeleteTestServer(t)
	// 建分类并上传图片
	if err := s.admin.CreateCategory(t.Context(), "delme"); err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}
	if _, err := s.admin.SaveFile(t.Context(), "delme", "a.png", strings.NewReader("png")); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	// 建引用该分类的栏目
	if err := s.addEndpoint("delme", "delme", "", "delme", ""); err != nil {
		t.Fatalf("addEndpoint: %v", err)
	}
	// 执行删除
	form := url.Values{"category": {"delme"}, "confirm_delete": {"1"}}
	req := httptest.NewRequest("POST", "/admin/category/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "已删除") {
		t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
	}
	// 目录已删
	if _, err := os.Stat(filepath.Join(s.dataDir, "delme")); !os.IsNotExist(err) {
		t.Fatalf("category dir should be removed")
	}
	// 栏目已下线
	s.mu.RLock()
	n := len(s.endpoints)
	s.mu.RUnlock()
	if n != 0 {
		t.Fatalf("endpoint referencing deleted category should be removed, got %d", n)
	}
}

// TestCategoryDeleteWithoutConfirm 未勾选确认时拒绝删除。
func TestCategoryDeleteWithoutConfirm(t *testing.T) {
	s, cookie := newDeleteTestServer(t)
	if err := s.admin.CreateCategory(t.Context(), "keepme"); err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}
	form := url.Values{"category": {"keepme"}}
	req := httptest.NewRequest("POST", "/admin/category/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "确认") {
		t.Fatalf("should reject without confirm_delete: %s", rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, "keepme")); err != nil {
		t.Fatalf("category dir should remain")
	}
}

// TestCategoryDeleteGuard 非法分类名与未登录请求防护。
func TestCategoryDeleteGuard(t *testing.T) {
	s, cookie := newDeleteTestServer(t)
	// 未登录 401
	req := httptest.NewRequest("POST", "/admin/category/delete", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	// 非法分类名
	form := url.Values{"category": {"../etc"}, "confirm_delete": {"1"}}
	req = httptest.NewRequest("POST", "/admin/category/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "不合法") {
		t.Fatalf("should reject invalid category name: %s", rec.Body.String())
	}
}
