package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestEvictPreviewCacheByBytes M6 验收：总字节超上限时按最旧优先淘汰。
func TestEvictPreviewCacheByBytes(t *testing.T) {
	root := t.TempDir()
	// 10 个文件，每个 1000 字节，mtime 依次递增
	for i := 0; i < 10; i++ {
		p := filepath.Join(root, "f"+string(rune('a'+i))+".webp")
		if err := os.WriteFile(p, make([]byte, 1000), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(time.Duration(i) * time.Minute)
		_ = os.Chtimes(p, mt, mt)
	}

	// 上限 3500 字节 → 只能留 3 个（最新 3 个）
	evictPreviewCacheFiles(root, 1000, 3500)
	ents, _ := os.ReadDir(root)
	if len(ents) != 3 {
		t.Fatalf("应保留 3 个文件，实际 %d", len(ents))
	}
	var total int64
	var names []string
	for _, e := range ents {
		info, _ := e.Info()
		total += info.Size()
		names = append(names, e.Name())
	}
	if total > 3500 {
		t.Fatalf("总大小 %d 超过上限", total)
	}
	// 留下的必须是最新的三个：h、i、j
	for _, want := range []string{"fh.webp", "fi.webp", "fj.webp"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("最新文件 %s 被误删，实际 %v", want, names)
		}
	}

	// 上限为 0 = 不限，但文件数上限仍生效
	evictPreviewCacheFiles(root, 2, 0)
	ents, _ = os.ReadDir(root)
	if len(ents) != 2 {
		t.Fatalf("文件数上限应生效，实际 %d", len(ents))
	}
}
