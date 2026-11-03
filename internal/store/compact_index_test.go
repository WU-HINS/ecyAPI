package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCompactIndexLifecycle M2b-2 验收：扫描后落盘、重启后校验一致、损坏时能重建。
func TestCompactIndexLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data", "categories")
	for c := 0; c < 3; c++ {
		for i := 0; i < 10; i++ {
			writePNG(t, filepath.Join(dir, "c"+pad(c), "p"+pad(i)+".png"), 4, 2)
		}
	}
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 5 * time.Second, SnapshotTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	if err := fs.saveSnapshot(); err != nil {
		t.Fatal(err)
	}
	idxPath := CompactIndexPath(dir)
	fi, err := os.Stat(idxPath)
	if err != nil {
		t.Fatalf("紧凑索引未生成: %v", err)
	}
	t.Logf("紧凑索引 %s，%d 字节，%d 条", filepath.Base(idxPath), fi.Size(), func() int {
		ok, n := fs.VerifyCompactIndex()
		if !ok {
			t.Fatalf("刚写完的索引校验失败")
		}
		return n
	}())
	fs.Close()

	// 重启：加载快照 + 校验紧凑索引
	st2, err := NewFileStoreWithOptions(dir, Options{StartupWait: 5 * time.Second, SnapshotTTL: time.Hour, SnapshotMaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs2 := st2.(*fileStore)
	defer fs2.Close()
	ok, n := fs2.VerifyCompactIndex()
	if !ok || n != 30 {
		t.Fatalf("重启后校验失败: ok=%v n=%d", ok, n)
	}

	// 破坏文件 → 校验必须失败（触发重建路径）
	if err := os.WriteFile(idxPath, []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _ := fs2.VerifyCompactIndex(); ok {
		t.Fatal("损坏的索引不应校验通过")
	}
}
