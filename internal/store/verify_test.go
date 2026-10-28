package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestVerifyRemovesDeadFile Verify 发现文件已删时从索引摘掉，且不 panic。
func TestVerifyRemovesDeadFile(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "categories")
	cat := filepath.Join(dataDir, "c")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.png", "z.png"} {
		if err := os.WriteFile(filepath.Join(cat, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := NewFileStoreWithOptions(dataDir, Options{ScanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if n, _ := st.Count(ctx, "c"); n != 2 {
		t.Fatalf("初始应为 2 张，得到 %d", n)
	}
	if err := os.Remove(filepath.Join(cat, "z.png")); err != nil {
		t.Fatal(err)
	}
	if st.Verify(ctx, "c", "z.png") {
		t.Fatal("已删除的文件 Verify 应为 false")
	}
	if n, _ := st.Count(ctx, "c"); n != 1 {
		t.Fatalf("摘除后应为 1 张，得到 %d", n)
	}
	if st.Verify(ctx, "c", "z.png") {
		t.Fatal("重复校验仍应为 false")
	}
	if !st.Verify(ctx, "c", "a.png") {
		t.Fatal("存在的文件应为 true")
	}
	// 路径穿越/非法名一律 false
	for _, bad := range []string{"", "../x", "a/b", "a\\b"} {
		if st.Verify(ctx, "c", bad) {
			t.Fatalf("%q 不应通过校验", bad)
		}
	}
}

// TestBackgroundRefresh 后台刷新在无请求时也会拾取新文件。
func TestBackgroundRefresh(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "categories")
	cat := filepath.Join(dataDir, "c")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := NewFileStoreWithOptions(dataDir, Options{ScanInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if n, _ := st.Count(ctx, "c"); n != 0 {
		t.Fatalf("初始应为空，得到 %d", n)
	}
	if err := os.WriteFile(filepath.Join(cat, "new.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := st.Count(ctx, "c"); n == 1 {
			return // 后台刷新已拾取，且客户端没有触发任何扫描
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("后台刷新未拾取新文件")
}
