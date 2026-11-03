package store

import (
	"context"
	"path/filepath"
	"testing"

	"ecyapi/internal/index"
)

// TestBuildCompactIndexParity 里程碑 2a：紧凑索引与内存索引内容一致。
func TestBuildCompactIndexParity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "categories")
	for i := 0; i < 3; i++ {
		for j := 0; j < 5; j++ {
			writePNG(t, filepath.Join(dir, "c"+pad(i), "p"+pad(j)+".png"), 4, 2)
		}
	}
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	defer fs.Close()

	out := filepath.Join(t.TempDir(), "index.bin")
	n, err := fs.BuildCompactIndex(out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 15 {
		t.Fatalf("条目数 = %d，期望 15", n)
	}

	ix, err := index.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	if ix.RecCount() != 15 {
		t.Fatalf("索引条目 = %d", ix.RecCount())
	}
	cats := ix.Categories()
	if len(cats) != 3 {
		t.Fatalf("分类数 = %d", len(cats))
	}
	for _, c := range cats {
		if c.Count != 5 {
			t.Fatalf("%s 条目 = %d", c.Name, c.Count)
		}
		// 内存里的条目与索引里的名字集合必须一致
		rows, err := st.Links(context.Background(), c.Name)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, r := range rows {
			seen[filepath.Base(r.Link)] = true
		}
		for i := 0; i < c.Count; i++ {
			rec, ok := ix.At(c, i)
			if !ok || !seen[rec.Name] {
				t.Fatalf("%s 索引条目 %q 在内存里找不到（%v）", c.Name, rec.Name, seen)
			}
			if _, ok := ix.Lookup(c, rec.Name); !ok {
				t.Fatalf("%s 查找 %q 失败", c.Name, rec.Name)
			}
		}
	}
}
