package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMemoryModeDropsOriMeta 内存模式下，索引就绪后 oriMeta 应被释放，
// 且二次扫描仍能正确判定方向（从索引读回）。
func TestMemoryModeDropsOriMeta(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data", "categories")
	wide := filepath.Join(dir, "c", "wide.png")
	tall := filepath.Join(dir, "c", "tall.png")
	writePNG(t, wide, 40, 20)
	writePNG(t, tall, 20, 40)

	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 10 * time.Second, SnapshotTTL: time.Hour, DropOriMeta: true})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	defer fs.Close()

	fs.mu.RLock()
	n := len(fs.oriMeta)
	fs.mu.RUnlock()
	if n != 0 {
		t.Fatalf("首次扫描后 oriMeta 应为空（已落到索引），实际 %d 个目录", n)
	}

	// 二次扫描：不应重新读文件头，且方向仍然正确
	if err := fs.scan(); err != nil {
		t.Fatal(err)
	}
	fs.mu.RLock()
	n2 := len(fs.oriMeta)
	fs.mu.RUnlock()
	if n2 != 0 {
		t.Fatalf("二次扫描后 oriMeta 仍应为空，实际 %d", n2)
	}

	for _, name := range []string{"wide.png", "tall.png"} {
		ori, w, h, ok := fs.oriFromIndex("c", name, fileSize(t, filepath.Join(dir, "c", name)), modNano(t, filepath.Join(dir, "c", name)))
		if !ok {
			t.Fatalf("%s 索引里查不到", name)
		}
		if w == 0 || h == 0 {
			t.Fatalf("%s 宽高缺失: %dx%d", name, w, h)
		}
		t.Logf("%s → %s %dx%d", name, ori, w, h)
		if name == "wide.png" && ori != OrientationLandscape {
			t.Fatalf("wide 方向错: %s", ori)
		}
		if name == "tall.png" && ori != OrientationPortrait {
			t.Fatalf("tall 方向错: %s", ori)
		}
	}
}

func fileSize(t *testing.T, p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func modNano(t *testing.T, p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime().UnixNano()
}
