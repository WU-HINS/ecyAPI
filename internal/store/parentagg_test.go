package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// makeParentWithFiles 造 parent/ 下 2 张图 + parent/sub/ 下 3 张图。
func makeParentWithFiles(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "categories")
	for _, p := range []string{"a.png", "b.png"} {
		writePNG(t, filepath.Join(dir, "parent", p), 40, 20)
	}
	for _, p := range []string{"x.png", "y.png", "z.png"} {
		writePNG(t, filepath.Join(dir, "parent", "sub", p), 20, 40)
	}
	return dir
}

// TestParentAggWithFiles 默认模式：父目录自己有图时，聚合 = 自己的 + 子目录的。
func TestParentAggWithFiles(t *testing.T) {
	dir := makeParentWithFiles(t)
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if n, _ := st.CountWith(ctx, "parent", OrientationAny); n != 5 {
		t.Fatalf("父目录应聚合 2+3=5 张，实际 %d", n)
	}
	if n, _ := st.CountWith(ctx, "parent", OrientationLandscape); n != 2 {
		t.Fatalf("父目录横图应为自身 2 张，实际 %d", n)
	}
	if n, _ := st.CountWith(ctx, "parent", OrientationPortrait); n != 3 {
		t.Fatalf("父目录竖图应来自子目录 3 张，实际 %d", n)
	}
	if n, _ := st.CountWith(ctx, "parent/sub", OrientationAny); n != 3 {
		t.Fatalf("子目录自身应是 3 张，实际 %d", n)
	}
	rows, err := st.Links(ctx, "parent")
	if err != nil || len(rows) != 5 {
		t.Fatalf("父目录列表应有 5 条，实际 %d（%v）", len(rows), err)
	}
	page, total, err := st.LinksPage(ctx, "parent", 0, 2)
	if err != nil || total != 5 || len(page) != 2 {
		t.Fatalf("分页应有 total=5 且取到 2 条，实际 total=%d len=%d err=%v", total, len(page), err)
	}
	// 随机取图应能取到父目录自身的图（前 2 条在聚合切片里）
	seen := map[string]bool{}
	for i := 0; i < 60; i++ {
		r, err := st.ByIndexWith(ctx, "parent", 1+i%5, OrientationAny)
		if err != nil {
			t.Fatal(err)
		}
		seen[r.ID] = true
	}
	if len(seen) != 5 {
		t.Fatalf("按序号应覆盖全部 5 张，实际 %d：%v", len(seen), seen)
	}
}

// TestParentAggDirsOnly dirs_only 模式：父目录自己有图时就是普通分类。
func TestParentAggDirsOnly(t *testing.T) {
	dir := makeParentWithFiles(t)
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0, ParentAggDirsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if n, _ := st.CountWith(ctx, "parent", OrientationAny); n != 2 {
		t.Fatalf("dirs_only 下父目录应只有自身 2 张，实际 %d", n)
	}
	if n, _ := st.CountWith(ctx, "parent/sub", OrientationAny); n != 3 {
		t.Fatalf("子目录应是 3 张，实际 %d", n)
	}
	// 无文件的父目录仍然聚合
	dir2 := filepath.Join(t.TempDir(), "categories")
	writePNG(t, filepath.Join(dir2, "p2", "c1", "a.png"), 10, 10)
	writePNG(t, filepath.Join(dir2, "p2", "c2", "b.png"), 10, 10)
	_ = os.MkdirAll(filepath.Join(dir2, "p2", "c1"), 0o755)
	st2, err := NewFileStoreWithOptions(dir2, Options{StartupWait: 0, ParentAggDirsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := st2.CountWith(context.Background(), "p2", OrientationAny); n != 2 {
		t.Fatalf("无文件的父目录仍应聚合子目录（2 张），实际 %d", n)
	}
}
