package store

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFileEntryStats M2b-1 验收：扫描写入的 size/mtime 与磁盘一致（抽查 100 条）。
func TestFileEntryStats(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "categories")
	for c := 0; c < 4; c++ {
		for i := 0; i < 50; i++ {
			writePNG(t, filepath.Join(dir, "c"+pad(c), "p"+pad(i)+".png"), 4, 2)
		}
	}
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 5 * time.Second, SnapshotTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	defer fs.Close()

	// 等首次扫描真正完成（StartupWait: 0 时扫描在后台）
	for i := 0; i < 300; i++ {
		if !fs.Scanning() {
			fs.mu.RLock()
			n := len(fs.cache)
			fs.mu.RUnlock()
			if n > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	type item struct{ cat, name string }
	var items []item
	fs.mu.RLock()
	for cat, set := range fs.cache {
		for _, e := range set.pick(OrientationAny) {
			items = append(items, item{cat, e.name})
		}
	}
	fs.mu.RUnlock()
	if len(items) != 200 {
		t.Fatalf("条目数 = %d", len(items))
	}

	rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	for _, it := range items[:100] {
		set, ok := fs.categorySet(it.cat)
		if !ok {
			t.Fatalf("分类 %s 不存在", it.cat)
		}
		var got fileEntry
		found := false
		for _, e := range set.pick(OrientationAny) {
			if e.name == it.name {
				got, found = e, true
				break
			}
		}
		if !found {
			t.Fatalf("%s/%s 找不到", it.cat, it.name)
		}
		fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(it.cat), it.name))
		if err != nil {
			t.Fatal(err)
		}
		if got.size != fi.Size() {
			t.Fatalf("%s/%s size = %d，磁盘 %d", it.cat, it.name, got.size, fi.Size())
		}
		if got.modNano != fi.ModTime().UnixNano() {
			t.Fatalf("%s/%s mtime = %d，磁盘 %d", it.cat, it.name, got.modNano, fi.ModTime().UnixNano())
		}
	}

	// 快照往返后仍保留 size/mtime
	if err := fs.saveSnapshot(); err != nil {
		t.Fatal(err)
	}
	st2, err := NewFileStoreWithOptions(dir, Options{StartupWait: 5 * time.Second, SnapshotTTL: time.Hour, SnapshotMaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs2 := st2.(*fileStore)
	defer fs2.Close()
	t.Logf("第二个 store 分类数=%d，快照=%s", fs2.loadedCategories(), fs2.snapshotPath())
	catName := items[0].cat
	if n, _ := st2.CountWith(context.Background(), catName, OrientationAny); n != 50 {
		t.Fatalf("恢复后 %s = %d", catName, n)
	}
	set, _ := fs2.categorySet(catName)
	for _, e := range set.pick(OrientationAny) {
		if e.size == 0 || e.modNano == 0 {
			t.Fatalf("快照恢复后条目缺 size/mtime：%+v", e)
		}
		break
	}
}
