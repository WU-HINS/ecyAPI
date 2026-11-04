package store

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// makeJPEG 生成指定像素尺寸的 JPEG。
func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withEXIFOrientation 往 JPEG 里插入 EXIF APP1（只放 Orientation 一项）。
func withEXIFOrientation(jpg []byte, orientation uint16) []byte {
	// TIFF 头（小端）+ 1 个 IFD 项：0x0112 Orientation SHORT 1 value
	tiff := new(bytes.Buffer)
	tiff.WriteString("MM\x00\x2a")                  // big-endian TIFF
	binary.Write(tiff, binary.BigEndian, uint32(8)) // IFD0 offset
	binary.Write(tiff, binary.BigEndian, uint16(1)) // 1 entry
	binary.Write(tiff, binary.BigEndian, uint16(0x0112))
	binary.Write(tiff, binary.BigEndian, uint16(3)) // SHORT
	binary.Write(tiff, binary.BigEndian, uint32(1))
	binary.Write(tiff, binary.BigEndian, uint16(orientation))
	binary.Write(tiff, binary.BigEndian, uint16(0)) // pad
	binary.Write(tiff, binary.BigEndian, uint32(0)) // next IFD = 0

	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := new(bytes.Buffer)
	seg.Write([]byte{0xFF, 0xE1})
	binary.Write(seg, binary.BigEndian, uint16(len(payload)+2))
	seg.Write(payload)

	// JPEG 以 FFD8 开头，插在 SOI 之后
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg.Bytes()...)
	out = append(out, jpg[2:]...)
	return out
}

// TestOrientationReality 实测：像素尺寸判定 vs EXIF 旋转的手机照片。
func TestOrientationReality(t *testing.T) {
	dir := t.TempDir()

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// 1) 普通横图 / 竖图
	landPath := write("land.jpg", makeJPEG(t, 200, 100))
	portPath := write("port.jpg", makeJPEG(t, 100, 200))
	_, _, o1 := detectOrientation(landPath)
	_, _, o2 := detectOrientation(portPath)
	t.Logf("普通横图 200x100 → %s", o1)
	t.Logf("普通竖图 100x200 → %s", o2)
	if o1 != OrientationLandscape || o2 != OrientationPortrait {
		t.Fatalf("常规尺寸判定就错了：%s / %s", o1, o2)
	}

	// 2) 手机竖拍：像素是横的 200x100，但 EXIF Orientation=6（顺时针转 90° 显示为竖）
	rotPath := write("rot.jpg", withEXIFOrientation(makeJPEG(t, 200, 100), 6))
	w, h, o3 := detectOrientation(rotPath)
	t.Logf("EXIF 旋转竖拍（像素 200x100, Orientation=6）→ 判定 %s（宽 %d 高 %d）", o3, w, h)
	if o3 == OrientationPortrait {
		t.Log("  ✓ 已正确处理 EXIF")
	} else {
		t.Logf("  ✗ 判成了 %s —— 实际显示方向是竖版", o3)
	}

	// 3) 方图
	sqPath := write("sq.jpg", makeJPEG(t, 120, 120))
	_, _, o4 := detectOrientation(sqPath)
	t.Logf("方图 120x120 → %s", o4)
}
