// preview.go — 服务端实时图片压缩预览。
//
// 首页背景与卡片缩略图的展示尺寸远小于壁纸级原图（常见 1080p~4K、1~4MB），
// 直接加载原图是首页流量的主要浪费。此模块按显示需求把图片等比缩放到指定宽度
// 再重新编码为压缩格式，结果落盘缓存，同一文件同一宽度只压缩一次。
//
// 仅影响压缩预览链路：/pic 原图输出不受影响，源图文件不改动。
// 输出保持源图格式（webp/jpeg/png），不改变扩展名与 Content-Type。
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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gen2brain/webp"
	"golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	xwebp "golang.org/x/image/webp"
)

// 预览宽度边界：低于 16px 无意义，高于 4096px 接近不压反扩，
// 上限同时挡住拿本接口当免费压图工具刷 CPU 的调用。
const (
	previewMinWidth = 16
	previewMaxWidth = 4096
)

// previewQueueLen 压缩队列容量。队列满时不排队等待：未命中缓存的请求
// 直接回退原图输出，保证接口永远有响应，只是偶尔多花点流量。
const previewQueueLen = 25

// previewCacheMaxFiles 缓存文件数量上限：超过后按修改时间删最旧的，
// 防止磁盘被压缩缩略图无限吃掉。每次新写缓存后异步清理一次。
const previewCacheMaxFiles = 2000

// previewQueue 压缩任务队列：容量即并发上限。
var previewQueue = make(chan struct{}, previewQueueLen)

// previewCacheRoot 预览缓存根目录，与数据目录同级：data/preview-cache/<分类>/。
func (s *Server) previewCacheRoot() string {
	return filepath.Join(filepath.Dir(s.dataDir), "preview-cache")
}

// handlePreview 输出压缩预览：/pic/{分类}/{文件}?w=640。
// 宽度由调用方自定义（16~4096），站内页面固定用 480 / 1280 两档。
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request, category, file, widthParam string) {
	width, err := strconv.Atoi(widthParam)
	if err != nil || width < previewMinWidth || width > previewMaxWidth {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.dataDir, category, file)
	ext, cachePath := s.previewCachePath(category, file, width)
	contentType := previewContentType(ext)

	// 缓存命中：直接输出（含 CDN 长缓存头）。
	if data := readCachedPreview(cachePath); data != nil {
		writePreview(w, data, contentType)
		return
	}

	// 队列满则不排队：无缓存可用，回退原图（w 参数已在 handlePic 入口移除）。
	select {
	case previewQueue <- struct{}{}:
	default:
		s.handlePic(w, r)
		return
	}
	// 拿到位置后再查一次：排队间隙可能已被并发请求填好缓存。
	if data := readCachedPreview(cachePath); data != nil {
		<-previewQueue
		writePreview(w, data, contentType)
		return
	}
	data, contentType, err := compressPreview(path, width, ext)
	<-previewQueue
	if err != nil {
		// 压缩失败（坏图/视频等不支持格式）不缓存错误，回退原图。
		s.handlePic(w, r)
		return
	}

	// 原子落盘：先写临时文件再 rename，避免并发读到半截文件。
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
		tmp := cachePath + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil && os.Rename(tmp, cachePath) == nil {
			// 落盘成功后异步清理超量缓存，不阻塞本次响应。
			go s.evictPreviewCache()
		}
	}

	writePreview(w, data, contentType)
}

// previewCachePath 计算缓存文件路径与源图扩展名。
// 缓存扩展名与源图一致：webp 源存 .webp、png 源存 .png，其余一律 .jpg。
func (s *Server) previewCachePath(category, file string, width int) (string, string) {
	ext := strings.ToLower(filepath.Ext(file))
	cacheExt := ".jpg"
	switch ext {
	case ".webp", ".png":
		cacheExt = ext
	}
	cache := filepath.Join(s.previewCacheRoot(), category, file+".w"+strconv.Itoa(width)+cacheExt)
	return ext, cache
}

// previewContentType 源扩展名对应的输出 Content-Type。
func previewContentType(ext string) string {
	switch ext {
	case ".webp":
		return "image/webp"
	case ".png":
		return "image/png"
	}
	return "image/jpeg"
}

// evictPreviewCache 缓存文件数超过上限时删掉最旧的。walk 全目录收集
// 修改时间，2000 以内的文件量排序开销可忽略；并发写导致偶发多删/少删
// 一个文件无关紧要，下次落盘会再触发清理，无需加锁。
func (s *Server) evictPreviewCache() {
	evictPreviewCacheFiles(s.previewCacheRoot(), previewCacheMaxFiles)
}

// evictPreviewCacheFiles 删除 root 下最旧的缓存文件，保留最新的 max 份。
func evictPreviewCacheFiles(root string, max int) {
	type cacheEntry struct {
		path    string
		modTime time.Time
	}
	var entries []cacheEntry
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		entries = append(entries, cacheEntry{path, info.ModTime()})
		return nil
	})
	if err != nil || len(entries) <= max {
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.Before(entries[j].modTime)
	})
	for _, e := range entries[:len(entries)-max] {
		_ = os.Remove(e.path)
	}
}

// cleanupPreviewCache 删除指定分类（或分类内单张图片）的压缩缓存。
// 后台删除图片/分类时调用，避免缓存里残留已删除图片的缩略图。
// file 为空表示清空整个分类的缓存。
func (s *Server) cleanupPreviewCache(category, file string) {
	root := filepath.Join(s.previewCacheRoot(), category)
	if file == "" {
		_ = os.RemoveAll(root)
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	prefix := file + ".w"
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			_ = os.Remove(filepath.Join(root, e.Name()))
		}
	}
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

	// 按源图格式重新编码：webp 源出 webp、png 源出 png，其余出 jpeg。
	// 保持格式不变是需求约束：调用方依赖扩展名/Content-Type 一致性。
	var buf bytes.Buffer
	switch ext {
	case ".webp":
		if err := webp.Encode(&buf, img, webp.Options{Quality: 82}); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/webp", nil
	case ".png":
		if err := png.Encode(&buf, img); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/png", nil
	}
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
