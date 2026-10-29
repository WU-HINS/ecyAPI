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
	"time"
)

// postAdmin 以登录态提交表单。
func postAdmin(t *testing.T, s *Server, cookie *http.Cookie, path string, form url.Values) string {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}

// cacheStatus 读取 /admin/cache/status。
func cacheStatus(t *testing.T, s *Server, cookie *http.Cookie) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/admin/cache/status", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("状态接口返回非法 JSON: %v / %s", err, rec.Body.String())
	}
	return out
}

// waitPrewarm 等预热结束。
func waitPrewarm(t *testing.T, s *Server, cookie *http.Cookie) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := cacheStatus(t, s, cookie)
		p, _ := st["prewarm"].(map[string]any)
		if p != nil && p["Running"] == false {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("预热超时未结束")
	return nil
}

// TestAdminCachePrewarmAndClear 预热生成图库页缓存与缩略图，清理后归零。
func TestAdminCachePrewarmAndClear(t *testing.T) {
	s := newGalleryServer(t, 20, nil)
	cookie := login(t, s, "pw")
	if cookie == nil {
		t.Fatal("登录失败")
	}

	// 预热：只渲染图库页
	body := postAdmin(t, s, cookie, "/admin/cache/prewarm", url.Values{"category": {"big"}, "width": {"0"}})
	if !strings.Contains(body, "预热已开始") {
		t.Fatalf("预热未启动: %s", body)
	}
	st := waitPrewarm(t, s, cookie)
	cacheInfo, _ := st["cache"].(map[string]any)
	if n, _ := cacheInfo["GalleryFiles"].(float64); n < 1 {
		t.Fatalf("图库缓存未生成: %+v", cacheInfo)
	}
	matches, _ := filepath.Glob(filepath.Join(s.galleryCacheRoot(), "*", "big.html"))
	if len(matches) != 1 {
		t.Fatalf("期望 1 个图库缓存文件，实际 %d", len(matches))
	}
	// gzip 版本也应生成
	if _, err := os.Stat(matches[0] + ".gz"); err != nil {
		t.Fatalf("gzip 版本未生成: %v", err)
	}

	// 预热缩略图
	body = postAdmin(t, s, cookie, "/admin/cache/prewarm", url.Values{"category": {"big"}, "width": {"64"}})
	if !strings.Contains(body, "预热已开始") {
		t.Fatalf("缩略图预热未启动: %s", body)
	}
	st = waitPrewarm(t, s, cookie)
	cacheInfo, _ = st["cache"].(map[string]any)
	if n, _ := cacheInfo["ThumbFiles"].(float64); n < 20 {
		t.Fatalf("缩略图数量不足: %+v", cacheInfo)
	}

	// 清理
	body = postAdmin(t, s, cookie, "/admin/cache/clear", url.Values{})
	if !strings.Contains(body, "缓存已清理") {
		t.Fatalf("清理失败: %s", body)
	}
	st = cacheStatus(t, s, cookie)
	cacheInfo, _ = st["cache"].(map[string]any)
	if g, _ := cacheInfo["GalleryFiles"].(float64); g != 0 {
		t.Fatalf("清理后仍有图库缓存: %+v", cacheInfo)
	}
	if th, _ := cacheInfo["ThumbFiles"].(float64); th != 0 {
		t.Fatalf("清理后仍有缩略图缓存: %+v", cacheInfo)
	}
}

// TestAdminCacheEndpointsRequireAuth 未登录不得操作缓存。
func TestAdminCacheEndpointsRequireAuth(t *testing.T) {
	s := newGalleryServer(t, 5, nil)
	for _, path := range []string{"/admin/cache/clear", "/admin/cache/prewarm"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 未登录应 401，得到 %d", path, rec.Code)
		}
	}
	// 状态接口是 GET
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/admin/cache/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status 未登录应 401，得到 %d", rec.Code)
	}
}
