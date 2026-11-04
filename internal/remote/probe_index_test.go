package remote

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ecyapi/internal/index"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 60, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestProbeIndex 批量探测：只探未知条目、回写方向位、二次运行不重复探测。
func TestProbeIndex(t *testing.T) {
	wide := pngBytes(t, 120, 60)
	tall := pngBytes(t, 60, 120)

	dav := newFakeDAV()
	dav.add("c/wide.png", string(wide))
	dav.add("c/tall.png", string(tall))
	srv := httptest.NewServer(dav)
	defer srv.Close()

	src, err := NewWebDAV(srv.URL+"/dav", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(t.TempDir(), "probe.bin")
	if _, err := BuildIndexFromSource(context.Background(), src, idxPath,
		IndexOptions{MaxDepth: 3, Extensions: []string{".png"}}); err != nil {
		t.Fatal(err)
	}

	ix, err := index.Open(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()

	// 初始全部未知
	cat, _ := ix.Find("c")
	for i := 0; i < cat.Count; i++ {
		rec, _ := ix.At(cat, i)
		if rec.Flags&0x3 != index.OriUnknown {
			t.Fatalf("初始应为未知，%s flags=%d", rec.Name, rec.Flags)
		}
	}

	res := ProbeIndex(context.Background(), ix, src, ProbeOptions{Workers: 2})
	if res.Probed != 2 || res.Failed != 0 {
		t.Fatalf("探测结果异常: %+v", res)
	}
	got := map[string]uint8{}
	for i := 0; i < cat.Count; i++ {
		rec, _ := ix.At(cat, i)
		got[rec.Name] = rec.Flags & 0x3
	}
	if got["wide.png"] != index.OriLand {
		t.Fatalf("wide 应为横，得到 %d", got["wide.png"])
	}
	if got["tall.png"] != index.OriPort {
		t.Fatalf("tall 应为竖，得到 %d", got["tall.png"])
	}

	// 二次运行：没有未知条目，应零探测
	res2 := ProbeIndex(context.Background(), ix, src, ProbeOptions{Workers: 2})
	if res2.Probed != 0 || res2.Unknown != 0 {
		t.Fatalf("二次运行不应再探测: %+v", res2)
	}
	_ = os.Remove(idxPath)
}
