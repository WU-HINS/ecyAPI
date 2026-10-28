package store

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writeTestPNG 写一张纯色 PNG，用于方向判定。
func writeTestPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 160, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestDetectOrientation 验证只读文件头判方向；非位图/损坏文件为 Unknown。
func TestDetectOrientation(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		w, h int
		want Orientation
	}{
		{"wide.png", 300, 100, OrientationLandscape},
		{"tall.png", 100, 300, OrientationPortrait},
		{"square.png", 120, 120, OrientationSquare},
	}
	for _, c := range cases {
		p := filepath.Join(dir, c.name)
		writeTestPNG(t, p, c.w, c.h)
		w, h, got := detectOrientation(p)
		if got != c.want || w != c.w || h != c.h {
			t.Fatalf("%s: (%d,%d,%v)，期望 (%d,%d,%v)", c.name, w, h, got, c.w, c.h, c.want)
		}
	}
	// 视频扩展名与损坏文件
	for _, name := range []string{"v.mp4", "s.svg", "broken.png"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("not a real image"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, got := detectOrientation(p); got != OrientationUnknown {
			t.Fatalf("%s 应判定为 unknown，得到 %v", name, got)
		}
	}
	if _, _, got := detectOrientation(filepath.Join(dir, "missing.png")); got != OrientationUnknown {
		t.Fatalf("不存在的文件应为 unknown")
	}
}

// TestParseOrientation 验证对外参数解析（中英文与简写）。
func TestParseOrientation(t *testing.T) {
	ok := map[string]Orientation{
		"": OrientationAny, "any": OrientationAny, "all": OrientationAny,
		"landscape": OrientationLandscape, "h": OrientationLandscape, "横": OrientationLandscape, "横版": OrientationLandscape,
		"portrait": OrientationPortrait, "v": OrientationPortrait, "竖": OrientationPortrait, "竖版": OrientationPortrait,
		"square": OrientationSquare, "LANDSCAPE": OrientationLandscape, " Portrait ": OrientationPortrait,
	}
	for in, want := range ok {
		got, valid := ParseOrientation(in)
		if !valid || got != want {
			t.Fatalf("ParseOrientation(%q) = (%v,%v)，期望 %v", in, got, valid, want)
		}
	}
	if _, valid := ParseOrientation("diagonal"); valid {
		t.Fatalf("无法识别的值应返回 false")
	}
}

// TestRandomWithOrientation 验证按方向筛选与序号语义。
func TestRandomWithOrientation(t *testing.T) {
	dir := t.TempDir()
	cat := filepath.Join(dir, "mixed")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPNG(t, filepath.Join(cat, "wide.png"), 300, 100)
	writeTestPNG(t, filepath.Join(cat, "tall.png"), 100, 300)
	writeTestPNG(t, filepath.Join(cat, "square.png"), 120, 120)
	// 无法判定的文件：只应出现在「不限方向」里
	if err := os.WriteFile(filepath.Join(cat, "broken.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if n, err := st.CountWith(ctx, "mixed", OrientationLandscape); err != nil || n != 1 {
		t.Fatalf("横版数量 = %d, err = %v", n, err)
	}
	if n, err := st.CountWith(ctx, "mixed", OrientationPortrait); err != nil || n != 1 {
		t.Fatalf("竖版数量 = %d, err = %v", n, err)
	}
	if n, err := st.Count(ctx, "mixed"); err != nil || n != 4 {
		t.Fatalf("总数 = %d, err = %v", n, err)
	}

	rows, err := st.RandomWith(ctx, "mixed", 5, OrientationLandscape)
	if err != nil || len(rows) != 1 || rows[0].ID != "wide.png" {
		t.Fatalf("横版随机 = %+v, err = %v", rows, err)
	}
	if rows[0].Orientation != "landscape" || rows[0].Width != 300 || rows[0].Height != 100 {
		t.Fatalf("Row 元信息 = %+v", rows[0])
	}
	rows, err = st.RandomWith(ctx, "mixed", 5, OrientationPortrait)
	if err != nil || len(rows) != 1 || rows[0].ID != "tall.png" {
		t.Fatalf("竖版随机 = %+v, err = %v", rows, err)
	}
	if _, err := st.RandomWith(ctx, "mixed", 1, OrientationSquare); err != ErrNotFound {
		t.Fatalf("正方形桶应为空（只进全集），得到 %v", err)
	}
	row, err := st.ByIndexWith(ctx, "mixed", 1, OrientationPortrait)
	if err != nil || row.ID != "tall.png" {
		t.Fatalf("按序取竖版 = %+v, err = %v", row, err)
	}
}

// TestOrientationRefreshedOnChange 验证文件被替换后方向索引会更新（mtime 变化即重判）。
func TestOrientationRefreshedOnChange(t *testing.T) {
	dir := t.TempDir()
	cat := filepath.Join(dir, "c")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(cat, "a.png")
	writeTestPNG(t, p, 300, 100)
	st, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	if n, _ := st.CountWith(context.Background(), "c", OrientationPortrait); n != 0 {
		t.Fatalf("初始应为 0 张竖版")
	}
	// 换成竖图（文件大小不同 + mtime 变化）
	os.Remove(p)
	writeTestPNG(t, p, 100, 300)
	if err := fs.reload(); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountWith(context.Background(), "c", OrientationPortrait); n != 1 {
		t.Fatalf("替换后应识别为 1 张竖版，得到 %d", n)
	}
	if n, _ := st.CountWith(context.Background(), "c", OrientationLandscape); n != 0 {
		t.Fatalf("替换后不应再有横版，得到 %d", n)
	}
}
