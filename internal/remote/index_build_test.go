package remote

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ecyapi/internal/index"
)

// TestBuildIndexFromWebDAV M3 验收：远端树 → 索引，分类带 FlagRemote，零下载。
func TestBuildIndexFromWebDAV(t *testing.T) {
	dav := newFakeDAV()
	dav.add("pc/a.png", "AAA")
	dav.add("pc/b.png", "BBBB")
	dav.add("pixiv/2024/x.png", "CCCCC")
	srv := httptest.NewServer(dav)
	defer srv.Close()

	src, err := NewWebDAV(srv.URL+"/dav", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "web.bin")
	n, err := BuildIndexFromSource(context.Background(), src, out, IndexOptions{MaxDepth: 3, Extensions: []string{".png"}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("条目数 = %d，期望 3", n)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("索引文件异常: %v", err)
	}

	ix, err := index.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	cats := ix.Categories()
	if len(cats) != 2 {
		t.Fatalf("分类数 = %d，期望 2（pc、pixiv/2024）", len(cats))
	}
	for _, c := range cats {
		if c.Flags&index.FlagRemote == 0 {
			t.Fatalf("分类 %s 未标 FlagRemote", c.Name)
		}
	}
	if c, ok := ix.Find("pc"); !ok || c.Count != 2 {
		t.Fatalf("pc 分类异常: ok=%v count=%d", ok, c.Count)
	}
}
