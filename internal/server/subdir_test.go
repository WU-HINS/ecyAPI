package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
)

// newSubdirServer 在临时目录里起服务：data/categories/a/b/f.png，并登录拿 cookie。
func newSubdirServer(t *testing.T) (*Server, *http.Cookie, string) {
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
	catDir := filepath.Join(dataDir, "a", "b")
	if err := os.MkdirAll(catDir, 0o755); err != nil {
		t.Fatalf("mkdir cat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(catDir, "f.png"), subdirPNG(t, 200, 120), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
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
	return s, cookie, catDir
}

func subdirPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png: %v", err)
	}
	return buf.Bytes()
}

// TestSplitPicPath 验证 /pic 路径拆分：最后一段是文件名，其余是分类路径。
func TestSplitPicPath(t *testing.T) {
	cases := []struct {
		path, cat, file string
		ok              bool
	}{
		{"/pic/pc/a.png", "pc", "a.png", true},
		{"/pic/a/b/f.png", "a/b", "f.png", true},
		{"/pic/a/b/c/f.png", "a/b/c", "f.png", true},
		{"/pic/a.png", "", "", false},
		{"/pic/", "", "", false},
		{"/pic/a/", "", "", false},
	}
	for _, c := range cases {
		cat, file, ok := splitPicPath(c.path)
		if ok != c.ok || cat != c.cat || file != c.file {
			t.Errorf("splitPicPath(%q) = (%q,%q,%v)，期望 (%q,%q,%v)",
				c.path, cat, file, ok, c.cat, c.file, c.ok)
		}
	}
}

// TestNestedPicServed 验证 /pic/a/b/f.png 直接输出原图。
func TestNestedPicServed(t *testing.T) {
	s, _, _ := newSubdirServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pic/a/b/f.png", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Body.Len() == 0 {
		t.Fatalf("响应体为空")
	}
}

// TestNestedPicPreview 验证多级分类的 ?w= 压缩预览与缓存落盘路径。
func TestNestedPicPreview(t *testing.T) {
	s, _, catDir := newSubdirServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/pic/a/b/f.png?w=64", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/webp" {
		t.Fatalf("Content-Type = %q", ct)
	}
	_ = catDir
	cache := filepath.Join(filepath.Dir(s.dataDir), "preview-cache", "a", "b", "f.png.w64.webp")
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("缓存文件未落在 %s: %v", cache, err)
	}
}

// TestNestedEndpointRedirect 验证 /a/b 栏目 302 到多级分类下的图片。
func TestNestedEndpointRedirect(t *testing.T) {
	s, _, _ := newSubdirServer(t)
	if err := s.addEndpoint("anime/2024", "a/b", "", "动漫2024", ""); err != nil {
		t.Fatalf("addEndpoint: %v", err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/anime/2024", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/pic/a/b/f.png" {
		t.Fatalf("Location = %q", loc)
	}
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, httptest.NewRequest("GET", "/anime/2024?json", nil))
	if body := rec2.Body.String(); !strings.Contains(body, "/pic/a/b/f.png") {
		t.Fatalf("纯文本输出 = %q", body)
	}
}

// TestNestedJSONAPI 验证 /json 支持 a/b 作为分类名。
func TestNestedJSONAPI(t *testing.T) {
	s, _, _ := newSubdirServer(t)
	for _, target := range []string{"/json?a/b", "/json?table=a/b", "/json?category=a/b&num=1"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 状态码 = %d", target, rec.Code)
		}
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s 返回非 JSON: %v / %q", target, err, rec.Body.String())
		}
		if payload["category"] != "a/b" || payload["count"].(float64) != 1 {
			t.Fatalf("%s 载荷 = %+v", target, payload)
		}
		data, ok := payload["data"].(map[string]any)
		if !ok {
			t.Fatalf("%s data 字段 = %+v", target, payload["data"])
		}
		if link, _ := data["link"].(string); !strings.HasSuffix(link, "/pic/a/b/f.png") {
			t.Fatalf("%s 链接 = %q", target, link)
		}
	}
}

// TestNestedAdminCreateAndDelete 验证后台创建 / 删除多级分类的完整链路。
func TestNestedAdminCreateAndDelete(t *testing.T) {
	s, cookie, _ := newSubdirServer(t)
	post := func(path string, form url.Values) string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	body := post("/admin/create", url.Values{
		"new_table_name":  {"deep/sub"},
		"new_table_label": {"深分类"},
	})
	if !strings.Contains(body, "创建成功") {
		t.Fatalf("创建多级分类失败: %s", body)
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, "deep", "sub")); err != nil {
		t.Fatalf("目录未创建: %v", err)
	}
	if n, err := s.st.Count(t.Context(), "deep/sub"); err != nil || n != 0 {
		t.Fatalf("Count = %d, err = %v", n, err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/deep/sub", nil))
	if rec.Code != http.StatusNotFound { // 空分类：无图可跳转
		t.Fatalf("空分类跳转状态码 = %d", rec.Code)
	}
	body = post("/admin/category/delete", url.Values{
		"category": {"deep/sub"}, "confirm_delete": {"1"},
	})
	if !strings.Contains(body, "已删除") {
		t.Fatalf("删除多级分类失败: %s", body)
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, "deep")); !os.IsNotExist(err) {
		t.Fatalf("空壳父目录未清理: %v", err)
	}
	s.mu.RLock()
	_, stillThere := s.epIndex["deep/sub"]
	s.mu.RUnlock()
	if stillThere {
		t.Fatalf("栏目 /deep/sub 未下线")
	}
}

// TestReservedAndInvalidPathsRejected 验证保留首段与非法路径在后台被拒。
func TestReservedAndInvalidPathsRejected(t *testing.T) {
	s, cookie, _ := newSubdirServer(t)
	post := func(path string, form url.Values) string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	cases := []struct {
		name string
		want string
	}{
		{"img", "内置接口"},
		{"pic/a", "内置接口"},
		{"a//b", "不合法"},
		{"../x", "不合法"},
		{"a/b/c/d", "不合法"},
	}
	for _, c := range cases {
		body := post("/admin/create", url.Values{"new_table_name": {c.name}, "new_table_label": {"x"}})
		if !strings.Contains(body, c.want) {
			t.Errorf("分类名 %q 的提示 = %s，应包含 %q", c.name, body, c.want)
		}
	}
	if err := s.addEndpoint("admin/x", "a/b", "", "x", ""); err == nil {
		t.Errorf("栏目路径 admin/x 应被拒绝")
	}
}

// TestNestedTraversalDenied 验证 /pic 下的穿越尝试不会逃出数据目录。
func TestNestedTraversalDenied(t *testing.T) {
	s, _, _ := newSubdirServer(t)
	for _, target := range []string{
		"/pic/a/../../data/endpoints.json",
		"/pic/..%2f..%2fdata%2fendpoints.json",
		"/pic/a/b/../../../etc/passwd",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", target, nil)
		s.Handler().ServeHTTP(rec, req) // ServeMux 先做路径清理（301）或直接 404
		if rec.Code == http.StatusOK {
			t.Fatalf("%s 不应成功返回内容", target)
		}
	}
}
