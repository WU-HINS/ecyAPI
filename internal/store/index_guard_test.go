package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ecyapi/internal/index"
)

// TestIndexRefusesShrink 扫描结果异常缩减时，不得覆盖磁盘索引。
func TestIndexRefusesShrink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data", "categories")
	for i := 0; i < 20; i++ {
		writePNG(t, filepath.Join(dir, "c", "p"+pad(i)+".png"), 8, 8)
	}
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	defer fs.Close()
	if err := fs.saveSnapshot(); err != nil {
		t.Fatal(err)
	}
	idxPath := CompactIndexPath(dir)
	before, err := index.Open(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	n0 := before.RecCount()
	before.Close()
	if n0 != 20 {
		t.Fatalf("索引初始应为 20 条，实际 %d", n0)
	}

	// 模拟一次「坏扫描」：内存里只剩 0 条
	fs.mu.Lock()
	fs.cache = map[string]*categorySet{}
	fs.mu.Unlock()
	fs.refreshCompactIndex()

	after, err := index.Open(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	if after.RecCount() != n0 {
		t.Fatalf("坏扫描把索引写坏了：%d → %d", n0, after.RecCount())
	}
	_ = os.Remove(idxPath)
}
