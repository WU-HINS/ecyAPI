package store

// orient.go — 图片横竖判定。
//
// 只需要宽高，因此用 image.DecodeConfig 读文件头（PNG/JPEG/GIF 几十字节，
// WebP 读 VP8/VP8L/VP8X 头），不解码像素，单文件耗时数十微秒。
// 视频、SVG、AVIF 与损坏文件返回 Unknown，不参与横竖筛选。
//
// 注意：Go 标准库不解 EXIF，手机竖拍照片若以「横向像素 + Orientation=6」
// 存储，会被判为横版；需要严格准确时必须额外读 EXIF 并交换宽高。

import (
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// Orientation 图片方向。
type Orientation int8

const (
	// OrientationAny 不限方向（默认）。
	OrientationAny Orientation = iota
	// OrientationLandscape 横版：宽 > 高。
	OrientationLandscape
	// OrientationPortrait 竖版：高 > 宽。
	OrientationPortrait
	// OrientationSquare 正方形：宽 == 高。
	OrientationSquare
	// OrientationUnknown 无法判定：视频 / SVG / AVIF / 损坏文件。
	OrientationUnknown
)

// String 返回对外展示名，与 JSON 字段 Row.Orientation 一致。
func (o Orientation) String() string {
	switch o {
	case OrientationLandscape:
		return "landscape"
	case OrientationPortrait:
		return "portrait"
	case OrientationSquare:
		return "square"
	case OrientationUnknown:
		return "unknown"
	}
	return "any"
}

// ParseOrientation 解析查询参数里的方向值，接受中英文与单字母简写。
// 空串等价 OrientationAny；无法识别时返回 (OrientationAny, false)。
func ParseOrientation(v string) (Orientation, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "any", "all", "both":
		return OrientationAny, true
	case "landscape", "horizontal", "h", "wide", "横", "横版":
		return OrientationLandscape, true
	case "portrait", "vertical", "v", "tall", "竖", "竖版":
		return OrientationPortrait, true
	case "square", "方", "方形":
		return OrientationSquare, true
	}
	return OrientationAny, false
}

// detectOrientation 读文件头判定方向；失败时宽高为 0、方向为 Unknown。
func detectOrientation(path string) (w, h int, o Orientation) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, OrientationUnknown
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, OrientationUnknown
	}
	switch {
	case cfg.Width > cfg.Height:
		return cfg.Width, cfg.Height, OrientationLandscape
	case cfg.Height > cfg.Width:
		return cfg.Width, cfg.Height, OrientationPortrait
	default:
		return cfg.Width, cfg.Height, OrientationSquare
	}
}
