package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
	"ecyapi/internal/remote"
)

// TestWebVideoAndGalleryDisabled 云端默认禁用视频代理与图库页。
func TestWebVideoAndGalleryDisabled(t *testing.T) {
	dav := newFakeDAV(t)
	dav.add("c/a.png", string(makePNG(t, 40, 20)))
	dav.add("c/movie.mp4", "MP4DATA")
	srv := httptest.NewServer(dav)
	defer srv.Close()

	src, _ := remote.NewWebDAV(srv.URL+"/dav", "", "", "")
	idx := filepath.Join(t.TempDir(), "web.bin")
	if _, err := remote.BuildIndexFromSource(context.Background(), src, idx,
		remote.IndexOptions{MaxDepth: 3, Extensions: []string{".png", ".mp4"}}); err != nil {
		t.Fatal(err)
	}

	// 默认：视频禁用
	s, err := New(config.Config{
		DataDir: t.TempDir(), AdminPassword: "pw", PreviewMP: 16,
		WebIndex: idx, WebKind: "webdav", WebURL: srv.URL + "/dav",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(t, s, "/@web/pic/c/movie.mp4"); rec.Code != http.StatusForbidden {
		t.Fatalf("视频应 403，得到 %d", rec.Code)
	}
	if rec := get(t, s, "/@web/pic/c/a.png"); rec.Code != http.StatusOK {
		t.Fatalf("图片应 200，得到 %d", rec.Code)
	}
	if rec := get(t, s, "/@web/img?category=c"); rec.Code != http.StatusNotFound {
		t.Fatalf("图库应 404，得到 %d", rec.Code)
	}
	// 随机不应选到视频（否则 302 后 403）
	for i := 0; i < 20; i++ {
		rec := get(t, s, "/@web/c")
		if rec.Code != http.StatusFound {
			continue
		}
		if strings.Contains(rec.Header().Get("Location"), ".mp4") {
			t.Fatalf("随机抽到了视频: %s", rec.Header().Get("Location"))
		}
	}

	// 开启后视频可用
	s2, _ := New(config.Config{
		DataDir: t.TempDir(), AdminPassword: "pw", PreviewMP: 16, WebVideo: true,
		WebIndex: idx, WebKind: "webdav", WebURL: srv.URL + "/dav",
	})
	if rec := get(t, s2, "/@web/pic/c/movie.mp4"); rec.Code != http.StatusOK {
		t.Fatalf("开启后视频应 200，得到 %d", rec.Code)
	}
}
