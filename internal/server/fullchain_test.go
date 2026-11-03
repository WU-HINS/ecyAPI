package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
	"ecyapi/internal/remote"
)

// TestFullChainCompactAndWeb M7 全链路冒烟：本地 compact 索引 + 云端 /@web 同时启用。
func TestFullChainCompactAndWeb(t *testing.T) {
	// 本地：3 张图
	dir := filepath.Join(t.TempDir(), "data", "categories")
	writeFilePNG(t, filepath.Join(dir, "pc", "a.png"), 40, 20)
	writeFilePNG(t, filepath.Join(dir, "pc", "b.png"), 20, 40)
	writeFilePNG(t, filepath.Join(dir, "pixiv", "x.png"), 40, 20)

	// 云端：2 张图（经 WebDAV）
	dav := newFakeDAV(t)
	dav.add("cloud1/u.png", string(makePNG(t, 40, 20)))
	dav.add("cloud1/v.png", string(makePNG(t, 20, 40)))
	srv := httptest.NewServer(dav)
	defer srv.Close()

	src, err := remote.NewWebDAV(srv.URL+"/dav", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	webIdx := filepath.Join(t.TempDir(), "web.bin")
	if _, err := remote.BuildIndexFromSource(context.Background(), src, webIdx,
		remote.IndexOptions{MaxDepth: 3, Extensions: []string{".png"}}); err != nil {
		t.Fatal(err)
	}

	s, err := New(config.Config{
		DataDir: dir, AdminPassword: "pw", PreviewMP: 16,
		CompactIndex: true, // 本地走紧凑索引
		WebIndex:     webIdx, WebKind: "webdav", WebURL: srv.URL + "/dav",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 本地：JSON / 302 / 父目录 / 图库
	if rec := get(t, s, "/json?pc"); rec.Code != http.StatusOK {
		t.Fatalf("/json?pc = %d", rec.Code)
	}
	if rec := get(t, s, "/pc"); rec.Code != http.StatusFound {
		t.Fatalf("/pc = %d", rec.Code)
	}
	if rec := get(t, s, "/pixiv"); rec.Code != http.StatusFound {
		t.Fatalf("父目录 /pixiv = %d", rec.Code)
	}
	if rec := get(t, s, "/img"); rec.Code != http.StatusOK {
		t.Fatalf("/img = %d", rec.Code)
	}

	// 云端：302 → 本域；代理出字节；JSON
	rec := get(t, s, "/@web/cloud1")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/@web/pic/cloud1/") {
		t.Fatalf("/@web/cloud1 = %d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	rec = get(t, s, "/@web/pic/cloud1/u.png")
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("代理出图失败: %d len=%d", rec.Code, rec.Body.Len())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("内容类型不对: %q", ct)
	}
	rec = get(t, s, "/@web/json?cloud1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/@web/pic/cloud1/") {
		t.Fatalf("/@web/json 返回异常: %d %q", rec.Code, rec.Body.String())
	}
}

// writeFilePNG 把生成的 PNG 写到磁盘（server 包里没有 store 的 writePNG 辅助）。
func writeFilePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, makePNG(t, w, h), 0o644); err != nil {
		t.Fatal(err)
	}
}
