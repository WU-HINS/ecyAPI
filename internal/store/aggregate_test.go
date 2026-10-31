package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestAggSqrtWeightByCount 单位权重 = sqrt(图片数)：100 张 vs 1 张 约 10:1（而不是 100:1）。
func TestAggSqrtWeightByCount(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "categories")
	for i := 0; i < 100; i++ {
		writePNG(t, filepath.Join(dir, "pixiv", "big", "b"+pad(i)+".png"), 4, 2)
	}
	writePNG(t, filepath.Join(dir, "pixiv", "small", "one.png"), 4, 2)

	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	counts := map[string]int{}
	const n = 2000
	for i := 0; i < n; i++ {
		rows, err := st.RandomWith(ctx, "pixiv", 1, OrientationAny)
		if err != nil || len(rows) != 1 {
			t.Fatalf("取图失败: %v", err)
		}
		parts := strings.Split(strings.TrimPrefix(rows[0].Link, "/pic/pixiv/"), "/")
		counts[parts[0]]++
	}
	big, small := counts["big"], counts["small"]
	if small < n/20 || small > n/5 { // 期望 ~9%，允许 5%~20%
		t.Fatalf("小目录占比应约 1/11，实际 %d/%d（%v）", small, n, counts)
	}
	if big < n*3/4 {
		t.Fatalf("大目录仍应占多数，实际 %d/%d", big, n)
	}
}

// TestNestedAggWeightedByCount 嵌套父目录按图片数（sqrt）加权：
// pixiv/2024/kuro(100 张) 与 pixiv/2025/x(1 张) → 约 10:1，而不是 1:1 或 100:1。
func TestNestedAggUniformPerLevel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "categories")
	for i := 0; i < 100; i++ {
		writePNG(t, filepath.Join(dir, "pixiv", "2024", "kuro", "k"+pad(i)+".png"), 4, 2)
	}
	writePNG(t, filepath.Join(dir, "pixiv", "2025", "x", "one.png"), 4, 2)

	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if n, _ := st.CountWith(ctx, "pixiv", OrientationAny); n != 101 {
		t.Fatalf("父聚合总数应为 101，实际 %d", n)
	}
	counts := map[string]int{}
	const n = 2000
	for i := 0; i < n; i++ {
		rows, err := st.RandomWith(ctx, "pixiv", 1, OrientationAny)
		if err != nil || len(rows) != 1 {
			t.Fatalf("取图失败: %v", err)
		}
		// /pic/pixiv/2024/kuro/k12.png → 取第二段（2024 / 2025）
		parts := strings.Split(strings.TrimPrefix(rows[0].Link, "/pic/pixiv/"), "/")
		counts[parts[0]]++
	}
	if counts["2025"] < n/20 || counts["2025"] > n/5 {
		t.Fatalf("小分支应约 1/11，实际 %d/%d（%v）", counts["2025"], n, counts)
	}
	if counts["2024"] < n*3/4 {
		t.Fatalf("大分支应占多数，实际 %d/%d", counts["2024"], n)
	}
}

// TestCategorySnapshotReuse 分类名与计数按代次复用，索引变了才重建。
func TestCategorySnapshotReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "categories")
	writePNG(t, filepath.Join(dir, "a", "1.png"), 4, 2)
	writePNG(t, filepath.Join(dir, "b", "1.png"), 4, 2)

	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0, ScanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	defer fs.Close()
	ctx := context.Background()

	first, _ := st.Categories(ctx)
	counts := st.Counts(ctx)
	if len(first) != 2 || counts["a"] != 1 {
		t.Fatalf("初始快照不对: %v %v", first, counts)
	}
	// 再取一次：应复用同一份底层数组
	second, _ := st.Categories(ctx)
	if len(first) > 0 && len(second) > 0 && &first[0] != &second[0] {
		t.Fatal("分类快照应复用同一份切片，实际重新分配了")
	}

	// 新增分类并重扫 → 代次变化，快照重建
	writePNG(t, filepath.Join(dir, "c", "1.png"), 4, 2)
	fs.reloadMu.Lock()
	if err := fs.scan(); err != nil {
		fs.reloadMu.Unlock()
		t.Fatal(err)
	}
	fs.reloadMu.Unlock()
	third, _ := st.Categories(ctx)
	if len(third) != 3 {
		t.Fatalf("重扫后应有 3 个分类，实际 %v", third)
	}
	if got := st.Counts(ctx)["c"]; got != 1 {
		t.Fatalf("新分类计数 = %d", got)
	}
}

// TestAggDepthExpansion 按 ECY_MAX_DEPTH 展开：更深的目录会成为独立单位，
// 2024 下有 5 个子目录各 1 张，2025 下 1 个子目录 100 张 —— 2024 与 2025 应各约 50%。
func TestAggUnitsAreSecondLevel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "categories")
	for i := 0; i < 5; i++ {
		writePNG(t, filepath.Join(dir, "pixiv", "2024", "leaf"+pad(i), "a.png"), 4, 2)
	}
	for i := 0; i < 100; i++ {
		writePNG(t, filepath.Join(dir, "pixiv", "2025", "only", "b"+pad(i)+".png"), 4, 2)
	}
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if n, _ := st.CountWith(ctx, "pixiv", OrientationAny); n != 105 {
		t.Fatalf("父聚合总数应为 105，实际 %d", n)
	}
	counts := map[string]int{}
	const n = 2000
	for i := 0; i < n; i++ {
		rows, err := st.RandomWith(ctx, "pixiv", 1, OrientationAny)
		if err != nil || len(rows) != 1 {
			t.Fatalf("取图失败: %v", err)
		}
		parts := strings.Split(strings.TrimPrefix(rows[0].Link, "/pic/pixiv/"), "/")
		counts[parts[0]]++
	}
	// 2024 展开成 5 个 1 张的单位（权重 5），2025 是 1 个 100 张的单位（权重 10）→ 约 1:2
	if counts["2024"] < n/5 || counts["2024"] > n/2 {
		t.Fatalf("2024 应按深度展开后约占 1/3，实际 %d/%d（%v）", counts["2024"], n, counts)
	}
}

// TestAggSmallDirNotStarved 极端差距下小目录仍能被随机到（1 张 vs 10000 张）。
func TestAggSmallDirNotStarved(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "categories")
	for i := 0; i < 200; i++ {
		writePNG(t, filepath.Join(dir, "pixiv", "big", "b"+pad(i)+".png"), 4, 2)
	}
	writePNG(t, filepath.Join(dir, "pixiv", "tiny", "one.png"), 4, 2)
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seen := 0
	const n = 3000
	for i := 0; i < n; i++ {
		rows, _ := st.RandomWith(ctx, "pixiv", 1, OrientationAny)
		if strings.Contains(rows[0].Link, "/tiny/") {
			seen++
		}
	}
	// sqrt 加权：1 vs sqrt(200)≈14 → 约 6.7%，纯按数量则是 0.5%
	if seen < n/40 { // 至少 2.5%
		t.Fatalf("小目录被挤占得太狠：%d/%d", seen, n)
	}
}
