package store

import (
	"os"
	"path/filepath"
	"testing"
)

// setupDir 造 n 张图的单分类目录。
func setupDir(b *testing.B, n int) (string, *fileStore) {
	b.Helper()
	dir := filepath.Join(b.TempDir(), "categories")
	cat := filepath.Join(dir, "pc")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		b.Fatal(err)
	}
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(cat, "f"+pad(i)+".png"), png, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0})
	if err != nil {
		b.Fatal(err)
	}
	return cat, st.(*fileStore)
}

func pad(i int) string {
	s := "00000"
	d := []byte(s)
	for j, k := len(d)-1, i; j >= 0 && k > 0; j, k = j-1, k/10 {
		d[j] = byte('0' + k%10)
	}
	return string(d)
}

// BenchmarkRefreshUnchanged 目录没变化时的后台刷新（走快速路径）。
func BenchmarkRefreshUnchanged(b *testing.B) {
	_, fs := setupDir(b, 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fs.reloadMu.Lock()
		if err := fs.scan(); err != nil {
			b.Fatal(err)
		}
		fs.reloadMu.Unlock()
	}
}

// BenchmarkRefreshOneAdded 加一张图后的刷新（整目录重建 + 复用方向缓存）。
func BenchmarkRefreshOneAdded(b *testing.B) {
	cat, fs := setupDir(b, 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		name := filepath.Join(cat, "new"+pad(i)+".png")
		if err := os.WriteFile(name, []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		fs.reloadMu.Lock()
		if err := fs.scan(); err != nil {
			b.Fatal(err)
		}
		fs.reloadMu.Unlock()
	}
}
