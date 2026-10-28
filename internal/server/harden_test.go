package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ecyapi/internal/config"
)

// newHardenedServer 建一个可登录的服务，分类 pc 内放 n 张图。
func newHardenedServer(t *testing.T, n int, mutate func(*config.Config)) (*Server, *http.Cookie) {
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
	cat := filepath.Join(dataDir, "pc")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatal(err)
	}
	png := subdirPNG(t, 32, 18)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("f%03d.png", i)
		if err := os.WriteFile(filepath.Join(cat, name), png, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{DataDir: dataDir, AdminPassword: "pw", MaxLinks: 50, MaxUploadBytes: 512 << 20}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, login(t, s, "pw")
}

func login(t *testing.T, s *Server, pw string) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin", strings.NewReader(url.Values{"login_password": {pw}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "ecy_admin" {
			return c
		}
	}
	return nil
}

// TestAdminLoginRateLimit 连续失败后锁定，成功登录会清除失败计数。
func TestAdminLoginRateLimit(t *testing.T) {
	s, _ := newHardenedServer(t, 0, nil)
	bad := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/admin", strings.NewReader(url.Values{"login_password": {"wrong"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < maxLoginFails; i++ {
		if rec := bad(); rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次失败应返回登录页，得到 %d", i+1, rec.Code)
		}
	}
	rec := bad()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("超过阈值应 429，得到 %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatalf("429 应带 Retry-After")
	}
	// 锁定期内即使口令正确也应被拒
	if c := login(t, s, "pw"); c != nil {
		t.Fatalf("锁定期内不应签发会话")
	}
	// 手动解锁后成功登录应清零计数
	s.login.reset("192.0.2.1")
	if c := login(t, s, "pw"); c == nil {
		t.Fatalf("解锁后应能登录")
	}
	s.login.mu.Lock()
	_, still := s.login.entries["192.0.2.1"]
	s.login.mu.Unlock()
	if still {
		t.Fatalf("成功登录后失败记录应被清除")
	}
}

// TestAdminDataPagination 大分类只取当前页，总数与页数正确。
func TestAdminDataPagination(t *testing.T) {
	s, cookie := newHardenedServer(t, 100, nil)
	type pageResp struct {
		Items      []map[string]any `json:"items"`
		Page       int              `json:"page"`
		Total      int              `json:"total"`
		TotalPages int              `json:"total_pages"`
	}
	get := func(page int) pageResp {
		req := httptest.NewRequest("GET", fmt.Sprintf("/admin/data?table=pc&page=%d", page), nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var payload pageResp
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("解析失败: %v / %s", err, rec.Body.String())
		}
		return payload
	}
	p2 := get(2)
	if p2.Total != 100 || p2.TotalPages != 3 || p2.Page != 2 || len(p2.Items) != 40 {
		t.Fatalf("第 2 页 = %+v（%d 条）", p2, len(p2.Items))
	}
	// 越界页自动收敛到最后一页
	p99 := get(99)
	if p99.Page != 3 || len(p99.Items) != 20 {
		t.Fatalf("越界页 = %+v（%d 条）", p99, len(p99.Items))
	}
}

// TestUploadBodyLimit 请求体超过配置上限时报错而不是写满磁盘。
func TestUploadBodyLimit(t *testing.T) {
	s, cookie := newHardenedServer(t, 0, func(c *config.Config) { c.MaxUploadBytes = 4096 })
	body := strings.Repeat("x", 8192)
	var b strings.Builder
	b.WriteString("--BOUNDARY\r\n")
	b.WriteString("Content-Disposition: form-data; name=\"category\"\r\n\r\n")
	b.WriteString("pc\r\n")
	b.WriteString("--BOUNDARY\r\n")
	b.WriteString("Content-Disposition: form-data; name=\"files\"; filename=\"big.png\"\r\n")
	b.WriteString("Content-Type: image/png\r\n\r\n")
	b.WriteString(body)
	b.WriteString("\r\n--BOUNDARY--\r\n")

	req := httptest.NewRequest("POST", "/admin/upload", strings.NewReader(b.String()))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=BOUNDARY")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "上传失败") {
		t.Fatalf("超限上传应报错: %s", rec.Body.String())
	}
	// 磁盘上不应留下文件
	entries, _ := os.ReadDir(filepath.Join(s.dataDir, "pc"))
	if len(entries) != 0 {
		t.Fatalf("超限请求不应落盘，实际 %d 个文件", len(entries))
	}
}

// TestSweepSessions 过期会话会被清理。
func TestSweepSessions(t *testing.T) {
	s, _ := newHardenedServer(t, 0, nil)
	s.sessions.Store("expired", time.Now().Add(-time.Minute))
	s.sessions.Store("alive", time.Now().Add(time.Hour))
	s.sweepSessions()
	if _, ok := s.sessions.Load("expired"); ok {
		t.Fatalf("过期会话未被清理")
	}
	if _, ok := s.sessions.Load("alive"); !ok {
		t.Fatalf("有效会话被误删")
	}
}
