package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
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

// TestSnapshotStaleIgnored 超过「最大有效期」的快照必须被忽略，回退到全量扫描。
func TestSnapshotStaleIgnored(t *testing.T) {
	dir := makeTree(t, 5, 2)
	st, _ := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	fs := st.(*fileStore)
	if err := fs.saveSnapshot(); err != nil {
		t.Fatal(err)
	}
	// 把快照内容里的 SavedAt 改成 2 小时前（有效期判定看的是它，不是文件 mtime）
	rewriteSnapshotTime(t, fs.snapshotPath(), time.Now().Add(-2*time.Hour))
	st2, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour, SnapshotMaxAge: 15 * time.Minute})
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

// TestSnapshotNewCategoryAfterRestart 停机期间新增的分类，重启后第一次刷新就会补上。
func TestSnapshotNewCategoryAfterRestart(t *testing.T) {
	dir := makeTree(t, 3, 2)
	st, _ := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	fs := st.(*fileStore)
	if err := fs.saveSnapshot(); err != nil {
		t.Fatal(err)
	}

	// 停机期间：新建一个分类，并在已有分类里加一张
	if err := os.MkdirAll(filepath.Join(dir, "c999"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(dir, "c999", "x.png"), 4, 2)
	writePNG(t, filepath.Join(dir, "c000", "zzz.png"), 4, 2)

	st2, _ := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	fs2 := st2.(*fileStore)
	ctx := context.Background()
	cats, _ := st2.Categories(ctx)
	if len(cats) != 3 {
		t.Fatalf("快照恢复时应只有 3 个分类，实际 %d：%v", len(cats), cats)
	}

	// 一次后台刷新（模拟 3 秒后的那一轮）
	fs2.reloadMu.Lock()
	if err := fs2.scan(); err != nil {
		fs2.reloadMu.Unlock()
		t.Fatal(err)
	}
	fs2.reloadMu.Unlock()

	cats, _ = st2.Categories(ctx)
	if len(cats) != 4 {
		t.Fatalf("刷新后应有 4 个分类（含新分类），实际 %d：%v", len(cats), cats)
	}
	if n, _ := st2.Count(ctx, "c999"); n != 1 {
		t.Fatalf("新分类里应有 1 张图，实际 %d", n)
	}
	if n, _ := st2.Count(ctx, "c000"); n != 3 {
		t.Fatalf("已有分类应变为 3 张，实际 %d", n)
	}
}

// TestSnapshotDeletedCategoryAfterRestart 停机期间删掉的分类，刷新后应消失。
func TestSnapshotDeletedCategoryAfterRestart(t *testing.T) {
	dir := makeTree(t, 3, 2)
	st, _ := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	fs := st.(*fileStore)
	if err := fs.saveSnapshot(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "c002")); err != nil {
		t.Fatal(err)
	}

	st2, _ := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	fs2 := st2.(*fileStore)
	ctx := context.Background()
	if cats, _ := st2.Categories(ctx); len(cats) != 3 {
		t.Fatalf("快照里仍是 3 个分类，实际 %d", len(cats))
	}
	fs2.reloadMu.Lock()
	if err := fs2.scan(); err != nil {
		fs2.reloadMu.Unlock()
		t.Fatal(err)
	}
	fs2.reloadMu.Unlock()
	cats, _ := st2.Categories(ctx)
	if len(cats) != 2 {
		t.Fatalf("删除后应剩 2 个分类，实际 %d：%v", len(cats), cats)
	}
}

// TestSnapshotRefreshedWhenIdle 索引没变化时，快照仍按 TTL 刷新时间戳。
// 否则「长期没人动」的库会因快照变旧，导致每次重启都退化成全量扫描。
func TestSnapshotRefreshedWhenIdle(t *testing.T) {
	dir := makeTree(t, 3, 2)
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: 40 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	// 先在测试结束前停掉后台循环，否则 t.TempDir 清理会和「每 40ms 重写快照」打架
	defer fs.Close()

	time.Sleep(150 * time.Millisecond) // 期间没有任何变更

	fh, err := os.Open(fs.snapshotPath())
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	zr, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatal(err)
	}
	var doc snapDoc
	if err := json.NewDecoder(zr).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if age := time.Since(doc.SavedAt); age > 120*time.Millisecond {
		t.Fatalf("快照未被周期刷新：已 %v 旧", age)
	}
}

// rewriteSnapshotTime 改写快照内容里的 SavedAt（模拟一份旧快照）。
func rewriteSnapshotTime(t *testing.T, path string, when time.Time) {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatal(err)
	}
	var doc snapDoc
	if err := json.NewDecoder(zr).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	_ = zr.Close()
	_ = fh.Close()
	doc.SavedAt = when
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(&doc); err != nil {
		t.Fatal(err)
	}
	_ = zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSnapshotOldButWithinMaxAge 默认 30 天有效期内，几小时前的快照仍然可用。
func TestSnapshotOldButWithinMaxAge(t *testing.T) {
	dir := makeTree(t, 5, 2)
	st, _ := NewFileStoreWithOptions(dir, Options{StartupWait: 0, SnapshotTTL: time.Hour})
	fs := st.(*fileStore)
	if err := fs.saveSnapshot(); err != nil {
		t.Fatal(err)
	}
	rewriteSnapshotTime(t, fs.snapshotPath(), time.Now().Add(-6*time.Hour))

	st2, err := NewFileStoreWithOptions(dir, Options{
		StartupWait: 0, SnapshotTTL: time.Hour,
		SnapshotMaxAge: 30 * 24 * time.Hour, // 默认值
	})
	if err != nil {
		t.Fatal(err)
	}
	if st2.Scanning() {
		t.Fatal("6 小时前的快照应被接受，不该触发扫描")
	}
	if cats, _ := st2.Categories(context.Background()); len(cats) != 5 {
		t.Fatalf("应恢复 5 个分类，实际 %d", len(cats))
	}
}
