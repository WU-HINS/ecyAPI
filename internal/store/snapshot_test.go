package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSnapshotRestore 快照能把分类完整恢复，且新实例不再扫盘。
func TestSnapshotRestore(t *testing.T) {
	dir := makeTree(t, 40, 5)
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	if err := fs.saveSnapshot(); err != nil {
		t.Fatalf("写快照失败: %v", err)
	}
	if _, err := os.Stat(fs.snapshotPath()); err != nil {
		t.Fatalf("快照文件不存在: %v", err)
	}

	// 新实例：应直接命中快照，构造后立刻有全部分类，且不在扫描
	start := time.Now()
	st2, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	cats, _ := st2.Categories(context.Background())
	if len(cats) != 40 {
		t.Fatalf("快照恢复后分类数 %d，期望 40", len(cats))
	}
	if st2.Scanning() {
		t.Fatal("快照恢复后不应在扫描")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("快照恢复过慢: %v", elapsed)
	}
	// 文件内容也要在
	n, _ := st2.Count(context.Background(), cats[0])
	if n != 5 {
		t.Fatalf("分类图片数 %d，期望 5", n)
	}
}

// TestSnapshotStaleIgnored 过期快照必须被忽略，回退到全量扫描。
func TestSnapshotStaleIgnored(t *testing.T) {
	dir := makeTree(t, 5, 2)
	st, _ := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	fs := st.(*fileStore)
	if err := fs.saveSnapshot(); err != nil {
		t.Fatal(err)
	}
	// 把快照改成 2 小时前
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(fs.snapshotPath(), old, old); err != nil {
		t.Fatal(err)
	}
	st2, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	cats, _ := st2.Categories(context.Background())
	if len(cats) != 5 {
		t.Fatalf("过期快照应回退全量扫描，分类数 %d", len(cats))
	}
}

// TestSnapshotCorruptFallsBack 快照损坏时不能让进程起不来。
func TestSnapshotCorruptFallsBack(t *testing.T) {
	dir := makeTree(t, 3, 2)
	st, _ := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	fs := st.(*fileStore)
	if err := os.WriteFile(fs.snapshotPath(), []byte("not a snapshot"), 0o644); err != nil {
		t.Fatal(err)
	}
	st2, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	if err != nil {
		t.Fatalf("损坏快照不应导致启动失败: %v", err)
	}
	cats, _ := st2.Categories(context.Background())
	if len(cats) != 3 {
		t.Fatalf("应回退到全量扫描，分类数 %d", len(cats))
	}
}

// TestSnapshotThenDiskChanged 停机期间新增的图片，恢复后由后台刷新补齐。
func TestSnapshotThenDiskChanged(t *testing.T) {
	dir := makeTree(t, 4, 2)
	st, _ := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	fs := st.(*fileStore)
	if err := fs.saveSnapshot(); err != nil {
		t.Fatal(err)
	}
	// 停机期间往已有分类里加一张图
	cat := filepath.Join(dir, "c000")
	if err := os.WriteFile(filepath.Join(cat, "zzz.png"), []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatal(err)
	}

	st2, _ := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	fs2 := st2.(*fileStore)
	if n, _ := st2.Count(context.Background(), "c000"); n != 2 {
		t.Fatalf("快照恢复后应先看到 2 张，实际 %d", n)
	}
	// 一次刷新即可纠正（名字变了 → 重建该目录）
	fs2.reloadMu.Lock()
	err := fs2.scan()
	fs2.reloadMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := st2.Count(context.Background(), "c000"); n != 3 {
		t.Fatalf("刷新后应为 3 张，实际 %d", n)
	}
}
