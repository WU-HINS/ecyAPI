package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupAggTree 构造 pixiv/{a,b} 与一个自身有图的三级目录，返回 store。
func setupAggTree(t *testing.T) Store {
	t.Helper()
	base := t.TempDir()
	dataDir := filepath.Join(base, "categories")
	writeDir := func(rel string, n int) {
		p := filepath.Join(dataDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			name := filepath.Join(p, fmt.Sprintf("f%d.png", i))
			if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeDir("pixiv/a", 2)
	writeDir("pixiv/b", 1)
	writeDir("pixiv/c/2024", 3)
	st, err := NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestParentAggregate 父目录（自身无图）聚合子分类，可计数/随机/按序取图。
func TestParentAggregate(t *testing.T) {
	st := setupAggTree(t)
	ctx := context.Background()

	n, err := st.CountWith(ctx, "pixiv", OrientationAny)
	if err != nil || n != 6 {
		t.Fatalf("父目录总数 = %d, err = %v（期望 6）", n, err)
	}
	// 子分类计数不变
	if n, _ := st.Count(ctx, "pixiv/a"); n != 2 {
		t.Fatalf("pixiv/a = %d", n)
	}
	if n, _ := st.Count(ctx, "pixiv/c/2024"); n != 3 {
		t.Fatalf("pixiv/c/2024 = %d", n)
	}

	rows, err := st.RandomWith(ctx, "pixiv", 4, OrientationAny)
	if err != nil || len(rows) != 4 {
		t.Fatalf("父目录随机 = %d 条, err = %v", len(rows), err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.ID+r.Link] {
			t.Fatalf("重复行 %+v", r)
		}
		seen[r.ID+r.Link] = true
		if !strings.HasPrefix(r.Link, "/pic/pixiv/") {
			t.Fatalf("链接路径不对: %s", r.Link)
		}
	}
	// 按序号取图
	if row, err := st.ByIndexWith(ctx, "pixiv", 6, OrientationAny); err != nil || row.Link == "" {
		t.Fatalf("按序取第 6 张失败: %+v, %v", row, err)
	}
	if _, err := st.ByIndexWith(ctx, "pixiv", 7, OrientationAny); err != ErrNotFound {
		t.Fatalf("越界应 ErrNotFound，得到 %v", err)
	}
	// 分类列表里不应出现父目录（避免图库重复计数）
	cats, _ := st.Categories(ctx)
	for _, c := range cats {
		if c == "pixiv" {
			t.Fatalf("父目录不应出现在分类列表: %v", cats)
		}
	}
	// Links 展开
	rows, err = st.Links(ctx, "pixiv")
	if err != nil || len(rows) != 6 {
		t.Fatalf("父目录 Links = %d 条, err = %v", len(rows), err)
	}
}

// TestAggregateIgnoresEmptyChildren 空子目录不应让父目录"假装有图"。
func TestAggregateIgnoresEmptyChildren(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "categories")
	if err := os.MkdirAll(filepath.Join(dataDir, "empty", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RandomWith(context.Background(), "empty", 1, OrientationAny); err != ErrNotFound {
		t.Fatalf("全空父目录应 ErrNotFound，得到 %v", err)
	}
}
