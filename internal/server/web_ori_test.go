package server

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"ecyapi/internal/config"
	"ecyapi/internal/index"
	"ecyapi/internal/remote"
)

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestWebLazyOrientation M5 验收：代理一次后，索引里的方向位从「未知」变「横」。
func TestWebLazyOrientation(t *testing.T) {
	land := makePNG(t, 40, 20) // 横图
	port := makePNG(t, 20, 40) // 竖图
	dav := newFakeDAV(t)
	dav.add("pc/wide.png", string(land))
	dav.add("pc/tall.png", string(port))
	srv := httptest.NewServer(dav)
	defer srv.Close()

	src, err := remote.NewWebDAV(srv.URL+"/dav", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(t.TempDir(), "web.bin")
	if _, err := remote.BuildIndexFromSource(context.Background(), src, idxPath,
		remote.IndexOptions{MaxDepth: 3, Extensions: []string{".png"}}); err != nil {
		t.Fatal(err)
	}

	s, err := New(config.Config{
		DataDir: t.TempDir(), AdminPassword: "pw", PreviewMP: 16,
		WebIndex: idxPath, WebKind: "webdav", WebURL: srv.URL + "/dav",
	})
	if err != nil {
		t.Fatal(err)
	}

	flagsOf := func(name string) uint8 {
		cat, ok := s.web.ix.Find("pc")
		if !ok {
			t.Fatal("pc 不存在")
		}
		rec, _, ok := s.web.ix.LookupPos(cat, name)
		if !ok {
			t.Fatalf("%s 不存在", name)
		}
		return rec.Flags & 0x3
	}
	if flagsOf("wide.png") != index.OriUnknown {
		t.Fatal("初始应为未知")
	}

	// 代理一次横图
	rec := get(t, s, "/@web/pic/pc/wide.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("代理失败: %d", rec.Code)
	}
	if flagsOf("wide.png") != index.OriLand {
		t.Fatalf("横图判定失败，flags=%d", flagsOf("wide.png"))
	}

	// 竖图
	if rec := get(t, s, "/@web/pic/pc/tall.png"); rec.Code != http.StatusOK {
		t.Fatalf("代理失败: %d", rec.Code)
	}
	if flagsOf("tall.png") != index.OriPort {
		t.Fatalf("竖图判定失败，flags=%d", flagsOf("tall.png"))
	}

	// 第二次访问不再重复判定（flags 保持不变），且仍能出图
	if rec := get(t, s, "/@web/pic/pc/wide.png"); rec.Code != http.StatusOK {
		t.Fatalf("二次代理失败: %d", rec.Code)
	}
	if flagsOf("wide.png") != index.OriLand {
		t.Fatal("二次访问不应改变已判定的方向")
	}
}
