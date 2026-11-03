package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestHeapCompactMode(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	const perCat, cats = 500, 100
	dir := filepath.Join(t.TempDir(), "data", "categories")
	src := filepath.Join(dir, "..", "src.png")
	_ = os.MkdirAll(filepath.Dir(src), 0o755)
	writePNG(t, src, 4, 2)
	for c := 0; c < cats; c++ {
		d := filepath.Join(dir, fmt.Sprintf("c%03d", c))
		_ = os.MkdirAll(d, 0o755)
		for i := 0; i < perCat; i++ {
			_ = os.Link(src, filepath.Join(d, fmt.Sprintf("f%04d.png", i)))
		}
	}
	runtime.GC()
	var b runtime.MemStats
	runtime.ReadMemStats(&b)

	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 10 * time.Second, SnapshotTTL: time.Hour, CompactRead: true})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	defer fs.Close()
	if err := fs.saveSnapshot(); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var a runtime.MemStats
	runtime.ReadMemStats(&a)
	t.Logf("compact 模式 5 万条：堆 %.1f MB（%.0f B/条），oriMeta 是否为 nil=%v",
		float64(int64(a.HeapAlloc)-int64(b.HeapAlloc))/1048576,
		float64(int64(a.HeapAlloc)-int64(b.HeapAlloc))/50000, fs.oriMeta == nil)
}
