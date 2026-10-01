// preview.go — 服务端实时图片压缩预览。
//
// 首页背景与卡片缩略图的展示尺寸远小于壁纸级原图（常见 1080p~4K、1~4MB），
// 直接加载原图是首页流量的主要浪费。此模块按显示需求把图片等比缩放到常规宽度
// 再重新编码为压缩格式，结果落盘缓存，同一文件同一宽度只压缩一次。
//
// 仅影响站内预览链路：/pic 原图输出与对外 API 完全不受影响，源图文件不改动。
package server

import (
	"bytes"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	xwebp "golang.org/x/image/webp"
)

// previewWidths 允许的预览宽度档位（px）。
// 480：首页卡片/图库缩略（展示列宽约 280~420，2x 屏幕够用）
// 1280：首页整屏背景（cover 展示，2x 屏幕够用）
var previewWidths = map[int]bool{480: true, 1280: true}

// previewSema 压缩并发上限：解码 4K 图约 50~150ms，4 并发已足够
// 首页十几个卡片的冷启动，同时避免大流量下 CPU 被打满。
var previewSema = make(chan struct{}, 4)

// previewCacheRoot 预览缓存根目录，与数据目录同级：data/preview-cache/<分类>/。
func (s *Server) previewCacheRoot() string {
	return filepath.Join(filepath.Dir(s.dataDir), "preview-cache")
}

// handlePreview 输出压缩预览：/pic/{分类}/{文件}?w=480。
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request, path, file, widthParam string) {
	width, err := strconv.Atoi(widthParam)
	if err != nil || !previewWidths[width] {
		http.NotFound(w, r)
		return
	}
	ext, cachePath := s.previewCachePath(path, file, width)

	// 缓存命中：直接输出（含 CDN 长缓存头）。
	if data := readCachedPreview(cachePath); data != nil {
		writePreview(w, data, "image/jpeg")
		return
	}

	// 未命中：加闸压缩；等闸期间可能已被并发请求填好缓存，再查一次。
	previewSema <- struct{}{}
	if data := readCachedPreview(cachePath); data != nil {
		<-previewSema
		writePreview(w, data, "image/jpeg")
		return
	}
	data, contentType, err := compressPreview(path, width, ext)
	<-previewSema
	if err != nil {
		// 压缩失败（坏图/视频等不支持格式）不缓存错误，回退原图。
		// w 参数已在 handlePic 入口移除，这里直接走原图输出，不会递归。
		s.handlePic(w, r)
		return
	}

	// 原子落盘：先写临时文件再 rename，避免并发读到半截文件。
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
		tmp := cachePath + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			_ = os.Rename(tmp, cachePath)
		}
	}

	writePreview(w, data, contentType)
}

// previewCachePath 计算缓存文件路径与源图扩展名。
func (s *Server) previewCachePath(path, file string, width int) (string, string) {
	ext := strings.ToLower(filepath.Ext(path))
	name := filepath.Base(path) // 分类名，已通过 IsValidIdentifier 校验
	cache := filepath.Join(s.previewCacheRoot(), name, file+".w"+strconv.Itoa(width)+".jpg")
	return ext, cache
}

// readCachedPreview 读缓存。
func readCachedPreview(cachePath string) []byte {
	data, err := os.ReadFile(cachePath)
	if err != nil || len(data) == 0 {
		return nil
	}
	return data
}

// writePreview 输出预览图片（带 CDN 长缓存头）。
func writePreview(w http.ResponseWriter, data []byte, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=604800, stale-while-revalidate=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// compressPreview 解码源图 → 等比缩放 → 重编码压缩。
// GIF 取第一帧；视频与 SVG 不支持，返回错误由调用方回退原图。
func compressPreview(path string, width int, ext string) ([]byte, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	img, err := decodeImage(f, ext)
	if err != nil {
		return nil, "", err
	}
	b := img.Bounds()
	if b.Dx() > width {
		// 等比缩到目标宽度；小图不放大。
		height := b.Dy() * width / b.Dx()
		if height < 1 {
			height = 1
		}
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		img = dst
	}

	// 有损 q82：主观画质与原图几乎无差，体积大幅下降。
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}

// decodeImage 按扩展名选择解码器；GIF 取第一帧。
func decodeImage(f *os.File, ext string) (image.Image, error) {
	switch ext {
	case ".jpg", ".jpeg":
		return jpeg.Decode(f)
	case ".png":
		return png.Decode(f)
	case ".webp":
		return xwebp.Decode(f)
	case ".bmp":
		return bmp.Decode(f)
	case ".gif":
		return gif.Decode(f) // 取第一帧
	}
	return nil, errUnsupportedPreview
}

var errUnsupportedPreview = errors.New("unsupported preview format")
