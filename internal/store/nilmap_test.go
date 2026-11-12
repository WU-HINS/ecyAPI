package store

import (
	"path/filepath"
	"testing"
	"time"
)

// TestScanAfterDropOriMeta 回归：dropOriMeta 之后再次扫描不得 panic。
// 线上曾因此 CrashLoop（assignment to entry in nil map）。
func TestScanAfterDropOriMeta(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "categories")
	writePNG(t, filepath.Join(dir, "c", "a.png"), 20, 10)

	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 10 * time.Second, DropOriMeta: true})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	defer fs.Close()

	// 模拟 oriMeta 被释放后的一次扫描
	fs.dropOriMeta()
	if err := fs.scan(); err != nil {
		t.Fatal(err)
	}
	// 再来一次（此时 oriMeta 非 nil 但为空）
	if err := fs.scan(); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountWith(t.Context(), "c", OrientationAny); n != 1 {
		t.Fatalf("扫描后应有 1 张，实际 %d", n)
	}
}
