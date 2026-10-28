package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ecyapi/internal/config"
)

// newDeadLinkServer 建一个含 a.png / z.png 的分类，且后台刷新间隔设得很长，
// 以便模拟「文件已删但索引还没刷新」的窗口。
func newDeadLinkServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "categories")
	cat := filepath.Join(dataDir, "c")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.png", "z.png"} {
		if err := os.WriteFile(filepath.Join(cat, n), subdirPNG(t, 32, 18), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(config.Config{
		DataDir: dataDir, AdminPassword: "pw",
		ScanIntervalSeconds: 3600, // 测试期间不让后台刷新介入
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestRedirectSkipsDeletedFile 目标文件被删后，302 必须换一张而不是指向死链。
func TestRedirectSkipsDeletedFile(t *testing.T) {
	s := newDeadLinkServer(t)
	dead := filepath.Join(s.dataDir, "c", "z.png")
	if err := os.Remove(dead); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/c", nil))
		if rec.Code != http.StatusFound {
			t.Fatalf("第 %d 次请求状态码 %d", i, rec.Code)
		}
		if loc := rec.Header().Get("Location"); !strings.HasSuffix(loc, "/pic/c/a.png") {
			t.Fatalf("第 %d 次跳到了死链: %s", i, loc)
		}
	}
	// 死链应已从索引摘除
	if n, _ := s.st.Count(t.Context(), "c"); n != 1 {
		t.Fatalf("索引中应只剩 1 张，实际 %d", n)
	}
}

// TestIndexLinkFallsBackWhenDead 按序号取到死链时回退为随机可用链接。
func TestIndexLinkFallsBackWhenDead(t *testing.T) {
	s := newDeadLinkServer(t)
	if err := os.Remove(filepath.Join(s.dataDir, "c", "z.png")); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/c?json=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "/pic/c/a.png") {
		t.Fatalf("应回退到存活的图片，实际 %q", body)
	}
}

var _ = time.Second
