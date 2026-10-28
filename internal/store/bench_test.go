package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setupBigCategory 造一个 n 张图的分类，用于测量扫描与随机路径。
func setupBigCategory(tb testing.TB, n int) (string, *fileStore) {
	tb.Helper()
	dir := tb.TempDir()
	cat := filepath.Join(dir, "big")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(cat, fmt.Sprintf("f%05d.png", i)), []byte("x"), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	st, err := NewFileStore(dir)
	if err != nil {
		tb.Fatal(err)
	}
	return dir, st.(*fileStore)
}

// BenchmarkScanCold 冷启动扫描（含每文件 stat+读头尝试）。
func BenchmarkScanCold(b *testing.B) {
	dir, fs := setupBigCategory(b, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fs2 := &fileStore{dir: dir, cache: map[string]*categorySet{}, oriMeta: map[string]map[string]orientMeta{}, dirs: map[string]*dirState{}}
		if err := fs2.scan(); err != nil {
			b.Fatal(err)
		}
	}
	_ = fs
}

// BenchmarkRescanStale 目录未变化时的节流重扫（热更新检查代价）。
func BenchmarkRescanStale(b *testing.B) {
	_, fs := setupBigCategory(b, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fs.lastScan.Store(0)
		fs.refreshIfStale()
	}
}

// BenchmarkRandomWith 热路径随机取 3 条。
func BenchmarkRandomWith(b *testing.B) {
	_, fs := setupBigCategory(b, 2000)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fs.RandomWith(ctx, "big", 3, OrientationAny); err != nil {
			b.Fatal(err)
		}
	}
	_ = time.Now
}
