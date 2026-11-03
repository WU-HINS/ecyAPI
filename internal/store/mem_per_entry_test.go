package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestIndexMemoryPerEntry 量化当前索引的内存占用（每条文件多少字节），
// 用来推算 100 万张图需要多少内存。
func TestIndexMemoryPerEntry(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	const perCat = 500
	const cats = 40 // 20000 条
	dir := filepath.Join(t.TempDir(), "categories")
	src := filepath.Join(dir, "src.png")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	writePNG(t, src, 4, 2)
	for c := 0; c < cats; c++ {
		d := filepath.Join(dir, fmt.Sprintf("c%02d", c))
		_ = os.MkdirAll(d, 0o755)
		for i := 0; i < perCat; i++ {
			_ = os.Link(src, filepath.Join(d, fmt.Sprintf("f%04d.png", i)))
		}
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	defer fs.Close()
	if n, _ := st.CountWith(t.Context(), "c00", OrientationAny); n != perCat {
		t.Fatalf("c00 = %d", n)
	}
	_ = fs

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	entries := cats * perCat
	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("条目 %d，堆增量 %.1f MB → 每条 %.0f 字节；推算 100 万条 ≈ %.0f MB",
		entries, float64(delta)/1048576, float64(delta)/float64(entries),
		float64(delta)/float64(entries)*1e6/1048576)
}
