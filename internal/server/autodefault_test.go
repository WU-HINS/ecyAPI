package server

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
)

// newServerWithEndpoints 用给定的 endpoints.json 内容起服务。
func newServerWithEndpoints(t *testing.T, body string) *Server {
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
	if body != "" {
		if err := os.WriteFile("data/endpoints.json", []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dataDir := filepath.Join(dir, "categories")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dataDir, AdminPassword: "pw"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// TestCreateCategoryDefaultsToAuto 后台新建分类默认就是「按设备自动分横竖」。
func TestCreateCategoryDefaultsToAuto(t *testing.T) {
	s := newServerWithEndpoints(t, "")
	cookie := login(t, s, "pw")
	post := func(form url.Values) string {
		req := httptest.NewRequest("POST", "/admin/create", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}

	if body := post(url.Values{"new_table_name": {"auto1"}, "new_table_label": {"默认"}}); !strings.Contains(body, "自动分横竖") {
		t.Fatalf("默认创建应提示自动分横竖: %s", body)
	}
	s.mu.RLock()
	ep1, ok1 := s.epIndex["auto1"]
	s.mu.RUnlock()
	if !ok1 || !ep1.auto || ep1.mobile != "" {
		t.Fatalf("默认创建应为自动横竖: %+v", ep1)
	}

	// 显式关闭后回到单端全量
	if body := post(url.Values{
		"new_table_name": {"plain1"}, "new_table_label": {"单端"}, "new_table_auto": {"0"},
	}); !strings.Contains(body, "不区分设备") {
		t.Fatalf("关闭自动后提示不符: %s", body)
	}
	s.mu.RLock()
	ep2, ok2 := s.epIndex["plain1"]
	s.mu.RUnlock()
	if !ok2 || ep2.auto {
		t.Fatalf("关闭后不应为自动横竖: %+v", ep2)
	}
}

// TestLoadEndpointsAutoDefault 旧 endpoints.json 未写 auto 时的默认推断。
func TestLoadEndpointsAutoDefault(t *testing.T) {
	body := `[
  {"path":"a","desk":"a"},
  {"path":"b","desk":"b","mobile":"m"},
  {"path":"v","desk":"v","kind":"video"},
  {"path":"c","desk":"c","auto":false},
  {"path":"d","desk":"d","mobile":"m","auto":true}
]`
	s := newServerWithEndpoints(t, body)
	s.mu.RLock()
	defer s.mu.RUnlock()
	cases := []struct {
		path string
		auto bool
		mob  string
	}{
		{"a", true, ""},   // 单端图片：默认开启
		{"b", false, "m"}, // 双端绑定：保持原语义
		{"v", false, ""},  // 视频：无方向
		{"c", false, ""},  // 显式 false：尊重文件
		{"d", true, ""},   // 显式 true 覆盖 mobile
	}
	for _, c := range cases {
		ep, ok := s.epIndex[c.path]
		if !ok {
			t.Fatalf("栏目 %s 未加载", c.path)
		}
		if ep.auto != c.auto || ep.mobile != c.mob {
			t.Fatalf("栏目 %s = auto:%v mobile:%q，期望 auto:%v mobile:%q", c.path, ep.auto, ep.mobile, c.auto, c.mob)
		}
	}
}

// TestEndpointsAutoPersistRoundTrip 显式 false 落盘后重新加载仍是 false。
func TestEndpointsAutoPersistRoundTrip(t *testing.T) {
	s := newServerWithEndpoints(t, "")
	if err := s.addEndpoint("plain", "plain", "", "单端", ""); err != nil {
		t.Fatalf("addEndpoint: %v", err)
	}
	raw, err := os.ReadFile("data/endpoints.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\"auto\": false") {
		t.Fatalf("落盘应显式写 auto:false，实际:\n%s", raw)
	}
	// 同一目录重新起服务，应保持 false 而不是被默认值改成 true
	s2, err := New(config.Config{DataDir: s.dataDir, AdminPassword: "pw"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s2.mu.RLock()
	ep, ok := s2.epIndex["plain"]
	s2.mu.RUnlock()
	if !ok || ep.auto {
		t.Fatalf("重新加载后 auto = %v（ok=%v），期望 false", ep.auto, ok)
	}
}
