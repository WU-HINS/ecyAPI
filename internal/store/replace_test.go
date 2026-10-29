package store

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
}

// TestReplaceInPlace 同名替换（内容、大小、mtime 都变）后，方向索引应更新。
func TestReplaceInPlace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "categories")
	cat := filepath.Join(dir, "pc")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(cat, "a.png"), 40, 20) // 横图

	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	ctx := context.Background()
	if n, _ := st.CountWith(ctx, "pc", OrientationLandscape); n != 1 {
		t.Fatalf("初始应为横图，实际 %d", n)
	}

	// 同名替换成竖图（更大、更新）
	time.Sleep(10 * time.Millisecond)
	writePNG(t, filepath.Join(cat, "a.png"), 20, 40)

	// 不带 forceVerify 的普通刷新：名字没变，走快速路径 → 暂时还认为是横图
	fs.reloadMu.Lock()
	_ = fs.scan()
	fs.reloadMu.Unlock()

	// 写路径的强制校验（或 60 秒后的强制完整校验）会重建并更新方向
	if err := fs.reload(); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountWith(ctx, "pc", OrientationPortrait); n != 1 {
		t.Fatalf("替换后应识别为竖图，实际 %d", n)
	}
	if n, _ := st.CountWith(ctx, "pc", OrientationLandscape); n != 0 {
		t.Fatalf("横图桶应清空，实际 %d", n)
	}
}

// TestReplaceSameSizeAndMtime 大小与 mtime 都没变的替换：不会被发现（文档化的边界）。
func TestReplaceSameSizeAndMtime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "categories")
	cat := filepath.Join(dir, "pc")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cat, "a.png")
	writePNG(t, path, 40, 20)
	orig, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	st, _ := NewFileStoreWithOptions(dir, Options{StartupWait: 0})
	fs := st.(*fileStore)
	ctx := context.Background()
	if n, _ := st.CountWith(ctx, "pc", OrientationLandscape); n != 1 {
		t.Fatalf("初始应为横图，实际 %d", n)
	}

	// 换成竖图，再把文件补齐到与原来同样的字节数，并还原 mtime
	writePNG(t, path, 20, 40)
	cur, _ := os.Stat(path)
	if pad := orig.Size() - cur.Size(); pad > 0 {
		fh, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fh.Write(make([]byte, pad))
		_ = fh.Close()
	}
	if err := os.Chtimes(path, orig.ModTime(), orig.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := fs.reload(); err != nil {
		t.Fatal(err)
	}
	// 边界：大小 + mtime 完全一致时索引不更新（图片内容仍以磁盘为准）
	if n, _ := st.CountWith(ctx, "pc", OrientationPortrait); n != 0 {
		t.Fatalf("大小+mtime 未变时不应重新判定，实际竖图 %d 张", n)
	}
}
