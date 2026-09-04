package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// setupTestDir 构造 N 个分类、每类 M 张图的测试目录。
func setupTestDir(tb testing.TB, cats, filesPerCat int) string {
	tb.Helper()
	dir := tb.TempDir()
	for c := 0; c < cats; c++ {
		cat := fmt.Sprintf("cat%d", c+1)
		sub := filepath.Join(dir, cat)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			tb.Fatal(err)
		}
		for i := 0; i < filesPerCat; i++ {
			name := fmt.Sprintf("img_%04d.webp", i)
			if err := os.WriteFile(filepath.Join(sub, name), []byte("x"), 0o644); err != nil {
				tb.Fatal(err)
			}
		}
	}
	return dir
}

func TestRandomUniqueness(t *testing.T) {
	dir := setupTestDir(t, 2, 100)
	st, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.Random(context.Background(), "cat1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 50 {
		t.Fatalf("want 50 rows, got %d", len(rows))
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.ID] {
			t.Fatalf("duplicate id %s", r.ID)
		}
		seen[r.ID] = true
	}
}

func TestRandomConcurrent(t *testing.T) {
	dir := setupTestDir(t, 4, 200)
	st, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				rows, err := st.Random(context.Background(), "cat1", 10)
				if err != nil || len(rows) != 10 {
					t.Errorf("random failed: %v len=%d", err, len(rows))
					return
				}
				if _, err := st.Categories(context.Background()); err != nil {
					t.Errorf("categories failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestWriteThenRead(t *testing.T) {
	dir := setupTestDir(t, 1, 5)
	st, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := NewFileAdmin(st)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := admin.CreateCategory(ctx, "newcat"); err != nil {
		t.Fatal(err)
	}
	// 写后立即读，无需等待节流窗口
	cats, err := st.Categories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cats {
		if c == "newcat" {
			found = true
		}
	}
	if !found {
		t.Fatalf("newcat not visible after create: %v", cats)
	}
}

func BenchmarkRandomParallel(b *testing.B) {
	dir := setupTestDir(b, 2, 1000)
	st, err := NewFileStore(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := st.Random(context.Background(), "cat1", 5); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkCategoriesParallel(b *testing.B) {
	dir := setupTestDir(b, 10, 100)
	st, err := NewFileStore(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := st.Categories(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
}
