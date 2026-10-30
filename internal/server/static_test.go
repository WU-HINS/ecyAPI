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

// newStaticServer 造一个最小实例用于静态资源测试。
func newStaticServer(t *testing.T, staticDir string) *Server {
	t.Helper()
	dir := t.TempDir()
	s, err := New(config.Config{
		DataDir: filepath.Join(dir, "categories"), AdminPassword: "pw",
		PreviewMP: 16, StaticDir: staticDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

// TestIndexStaticEmbedded 内置资源可直接访问。
func TestIndexStaticEmbedded(t *testing.T) {
	s := newStaticServer(t, filepath.Join(t.TempDir(), "empty"))

	cases := []struct{ path, ctype, must string }{
		{"/@index/dsh-pet.standalone.js", "application/javascript", "DSHPet"},
		{"/@index/assets/config.jsonc", "application/json", "animations"},
		{"/@index/assets/pic/cursor-grab.png", "image/png", ""},
	}
	for _, c := range cases {
		rec := get(t, s, c.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 状态码 %d", c.path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, c.ctype) {
			t.Fatalf("%s Content-Type=%q，期望含 %q", c.path, ct, c.ctype)
		}
		if c.must != "" && !strings.Contains(rec.Body.String(), c.must) {
			t.Fatalf("%s 内容不含 %q", c.path, c.must)
		}
	}
}

// TestIndexStaticDiskOverrides 磁盘目录优先于内置资源（放 webm 用）。
func TestIndexStaticDiskOverrides(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets", "webm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "webm", "待机呼吸休闲.webm"), []byte("WEBMDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStaticServer(t, dir)
	rec := get(t, s, "/@index/assets/webm/待机呼吸休闲.webm")
	if rec.Code != http.StatusOK || rec.Body.String() != "WEBMDATA" {
		t.Fatalf("磁盘资源未生效: %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "video/webm" {
		t.Fatalf("Content-Type=%q", ct)
	}
}

// TestIndexStaticNotFound 缺失文件与路径穿越都应 404。
func TestIndexStaticNotFound(t *testing.T) {
	s := newStaticServer(t, filepath.Join(t.TempDir(), "empty"))
	for _, p := range []string{
		"/@index/nope.js",
		"/@index/assets/webm/不存在.webm",
		"/@index/",
	} {
		if rec := get(t, s, p); rec.Code != http.StatusNotFound {
			t.Fatalf("%s 应 404，得到 %d", p, rec.Code)
		}
	}
	// 路径穿越：ServeMux 会先 307 归一化到 /etc/passwd，跟随之后必须 404。
	rec := get(t, s, "/@index/../../etc/passwd")
	if rec.Code == http.StatusTemporaryRedirect || rec.Code == http.StatusMovedPermanently {
		loc := rec.Header().Get("Location")
		if strings.Contains(loc, "/@index/") {
			t.Fatalf("归一化后仍指向 /@index/*: %s", loc)
		}
		if followed := get(t, s, loc); followed.Code != http.StatusNotFound {
			t.Fatalf("穿越目标 %s 应 404，得到 %d", loc, followed.Code)
		}
	} else if rec.Code != http.StatusNotFound {
		t.Fatalf("路径穿越应 404/307，得到 %d", rec.Code)
	}
}

// TestIndexStaticRange 支持 Range（视频拖动进度条）。
func TestIndexStaticRange(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets", "webm"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("0123456789")
	if err := os.WriteFile(filepath.Join(dir, "assets", "webm", "a.webm"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStaticServer(t, dir)
	req := httptest.NewRequest("GET", "/@index/assets/webm/a.webm", nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "2345" {
		t.Fatalf("Range 未生效: %d %q", rec.Code, rec.Body.String())
	}
}
