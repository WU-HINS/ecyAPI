package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// galleryCacheRoot 图库 HTML 缓存目录：data/preview-cache/gallery/。
// 图库要「展示全部」，单次渲染 5 万张要 2 秒 + 8MB；渲染结果按
// (索引代次, 隐藏集合, 抽样上限) 缓存成静态 HTML，命中时直接 sendfile，
// 100 并发也只占带宽，不再重复模板渲染。
func (s *Server) galleryCacheRoot() string {
	return filepath.Join(s.previewCacheRoot(), "gallery")
}

// galleryCacheKey 计算缓存目录名与文件名。
func (s *Server) galleryCacheKey(category string, gen uint64) (dir string, file string) {
	h := sha1.Sum([]byte(strings.Join(s.galleryHiddenList(), "\x00")))
	dir = fmt.Sprintf("%d-%s-%d", gen, hex.EncodeToString(h[:4]), s.cfg.GalleryMax)
	file = category + ".html"
	return dir, file
}

// serveGalleryCache 命中则直接输出缓存并返回 true。
// 同时准备了 gzip 版本：5 万张的图库 HTML 约 37MB，压缩后约 1MB，
// 100 并发时这是数量级的差别。
func (s *Server) serveGalleryCache(w http.ResponseWriter, r *http.Request, cachePath string) bool {
	useGzip := strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
	if useGzip {
		if s.serveGalleryFile(w, r, cachePath+".gz", true) {
			return true
		}
	}
	return s.serveGalleryFile(w, r, cachePath, false)
}

func (s *Server) serveGalleryFile(w http.ResponseWriter, r *http.Request, path string, gzipped bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil || fi.IsDir() || fi.Size() == 0 {
		_ = f.Close()
		return false
	}
	defer f.Close()
	// 图库页比图片变化频繁，给较短的客户端缓存。
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Add("Vary", "Accept-Encoding")
	if gzipped {
		w.Header().Set("Content-Encoding", "gzip")
	}
	serveLocalFile(w, r, f, fi, "text/html; charset=utf-8")
	return true
}

// writeGalleryCache 原子落盘，并清理旧代次的目录。
func (s *Server) writeGalleryCache(cachePath string, body []byte) {
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp := cachePath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, cachePath); err != nil {
		_ = os.Remove(tmp)
		return
	}
	// 预生成 gzip 版本：从刚写好的文件流式压缩，避免再占一份几十 MB 的缓冲。
	s.compressGalleryCache(cachePath)
	// 仅在缓存未命中时执行，代价是列一次目录，同步做以免旧缓存堆积。
	s.pruneGalleryCache(filepath.Base(dir))
}

// compressGalleryCache 把已落盘的 HTML 流式压缩成 .gz（BestSpeed）。
func (s *Server) compressGalleryCache(cachePath string) {
	src, err := os.Open(cachePath)
	if err != nil {
		return
	}
	defer src.Close()
	tmp := cachePath + ".gz.tmp"
	dst, err := os.Create(tmp)
	if err != nil {
		return
	}
	zw, _ := gzip.NewWriterLevel(dst, gzip.BestSpeed)
	if _, err := io.Copy(zw, src); err != nil || zw.Close() != nil || dst.Close() != nil {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, cachePath+".gz"); err != nil {
		_ = os.Remove(tmp)
	}
}

// pruneGalleryCache 删除非当前代次的缓存目录（内容已失效）。
func (s *Server) pruneGalleryCache(keep string) {
	root := s.galleryCacheRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

// renderGalleryHTML 把图库模板渲染到内存，供缓存落盘与首次响应复用。
func (s *Server) renderGalleryHTML(data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := s.tpl.ExecuteTemplate(&buf, "gallery.html", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
