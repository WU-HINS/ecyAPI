package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsValidCategory 表驱动校验分类路径规则：支持 a/b，拒绝穿越与非法段。
func TestIsValidCategory(t *testing.T) {
	valid := []string{"a", "ab", "a1", "a_b", "a/b", "a/b/c", "anime/2024", "pc/mobile", "a/2024/10"}
	for _, name := range valid {
		if !IsValidCategory(name) {
			t.Errorf("%q 应合法", name)
		}
	}
	invalid := []string{
		"", "/a", "a/", "a//b", "..", ".", "../etc", "a/../b", "a/./b",
		"1a", "a-b", "a.b", "a b", "a\\b", "a/b/c/d", "a/-b", "a/ b",
		strings.Repeat("a", 65), strings.Repeat("a/", 64) + "a",
	}
	for _, name := range invalid {
		if IsValidCategory(name) {
			t.Errorf("%q 应非法", name)
		}
	}
}

// TestNestedScan 验证递归扫描：a、a/b、a/c 三个分类，纯承载目录 x 不登记。
func TestNestedScan(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a/one.png")
	write("a/b/two.png")
	write("a/b/three.png")
	write("x/y/four.png") // x 只有子目录，不应成为分类

	st, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cats, err := st.Categories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"a": true, "a/b": true, "x/y": true}
	if len(cats) != len(want) {
		t.Fatalf("分类列表 = %v", cats)
	}
	for _, c := range cats {
		if !want[c] {
			t.Fatalf("出现意外分类 %q（全部：%v）", c, cats)
		}
	}
	if n, err := st.Count(context.Background(), "a/b"); err != nil || n != 2 {
		t.Fatalf("a/b 数量 = %d, err = %v", n, err)
	}
	rows, err := st.Random(context.Background(), "a/b", 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.Link, "/pic/a/b/") {
			t.Fatalf("链接应为 /pic/a/b/xxx，得到 %q", r.Link)
		}
	}
	if _, err := st.ByIndex(context.Background(), "a/b", 3); err != ErrNotFound {
		t.Fatalf("越界序号应 ErrNotFound，得到 %v", err)
	}
}

// TestCreateNestedCategory 验证多级分类自动建出上级目录，结束删除时清理空壳父目录。
func TestCreateNestedCategory(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin, err := NewFileAdmin(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.CreateCategory(ctx, "m/n"); err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "m", "n")); err != nil || !fi.IsDir() {
		t.Fatalf("多级目录未创建: %v", err)
	}
	cats, _ := st.Categories(ctx)
	if len(cats) != 1 || cats[0] != "m/n" {
		t.Fatalf("分类列表 = %v，应为 [m/n]", cats)
	}
	if _, err := admin.SaveFile(ctx, "m/n", "p.png", strings.NewReader("png")); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	if n, _ := st.Count(ctx, "m/n"); n != 1 {
		t.Fatalf("数量 = %d", n)
	}
	if err := admin.DeleteCategory(ctx, "m/n"); err != nil {
		t.Fatalf("DeleteCategory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "m")); !os.IsNotExist(err) {
		t.Fatalf("父目录 m 应被清理，err = %v", err)
	}
	if cats, _ := st.Categories(ctx); len(cats) != 0 {
		t.Fatalf("删除后分类列表应为空，得到 %v", cats)
	}
}

// TestNestedCategoryRejectsTraversal 验证非法路径在 store 层被拒绝。
func TestNestedCategoryRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := NewFileAdmin(st)
	ctx := context.Background()
	for _, name := range []string{"../escape", "a/../../b", "/abs", "a/", "a//b"} {
		if err := admin.CreateCategory(ctx, name); err == nil {
			t.Errorf("CreateCategory(%q) 应失败", name)
		}
		if _, err := admin.SaveFile(ctx, name, "x.png", strings.NewReader("x")); err == nil {
			t.Errorf("SaveFile(%q) 应失败", name)
		}
		if _, err := admin.DeleteFiles(ctx, name, []string{"x.png"}); err == nil {
			t.Errorf("DeleteFiles(%q) 应失败", name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); !os.IsNotExist(err) {
		t.Fatalf("不应在数据根外创建目录")
	}
}
