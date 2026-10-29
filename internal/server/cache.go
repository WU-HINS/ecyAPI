package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ecyapi/internal/store"
)

// errGalleryBusy 内存压力下拒绝渲染（与 handleGallery 的 503 对应）。
var errGalleryBusy = errors.New("gallery: 内存压力过高，暂停渲染")

// errPrewarmBusy 预热时资源紧张，需要退让重试。
var errPrewarmBusy = errors.New("prewarm: 资源紧张")

// cacheStats 缓存用量统计（字段名即 JSON 键）。
type cacheStats struct {
	GalleryFiles int
	GalleryBytes int64
	ThumbFiles   int
	ThumbBytes   int64
}

// cacheStats 统计图库 HTML 缓存与缩略图缓存的占用。
func (s *Server) cacheStats() cacheStats {
	var st cacheStats
	root := s.previewCacheRoot()
	walk := func(dir string, files *int, bytes *int64) {
		_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if fi, ierr := d.Info(); ierr == nil {
				*files++
				*bytes += fi.Size()
			}
			return nil
		})
	}
	walk(filepath.Join(root, "gallery"), &st.GalleryFiles, &st.GalleryBytes)
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != "gallery" {
				walk(filepath.Join(root, e.Name()), &st.ThumbFiles, &st.ThumbBytes)
			}
		}
	}
	return st
}

// clearCaches 删除缩略图与图库 HTML 缓存，返回清理前的统计。
func (s *Server) clearCaches() (cacheStats, error) {
	before := s.cacheStats()
	root := s.previewCacheRoot()
	if err := os.RemoveAll(root); err != nil {
		return before, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return before, err
	}
	return before, nil
}

// prewarmState 预热任务状态（同时只允许一个任务）。
type prewarmState struct {
	mu      sync.Mutex
	running bool
	cancel  bool
	done    int
	total   int
	current string
	errors  int
	started time.Time
	message string
}

// prewarmStatus 对外暴露的状态。
type prewarmStatus struct {
	Running   bool
	Done      int
	Total     int
	Current   string
	Errors    int
	StartedAt string
	Message   string
}

func (s *Server) prewarmSnapshot() prewarmStatus {
	p := s.prewarm
	p.mu.Lock()
	defer p.mu.Unlock()
	out := prewarmStatus{
		Running: p.running, Done: p.done, Total: p.total,
		Current: p.current, Errors: p.errors, Message: p.message,
	}
	if !p.started.IsZero() {
		out.StartedAt = p.started.Format(time.RFC3339)
	}
	return out
}

// startPrewarm 启动后台预热；返回 false 表示已有任务在跑。
func (s *Server) startPrewarm(cats []string, width int) bool {
	p := s.prewarm
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return false
	}
	p.running = true
	p.cancel = false
	p.done, p.total, p.errors, p.current = 0, len(cats), 0, ""
	p.started = time.Now()
	p.message = "进行中"
	p.mu.Unlock()

	go s.prewarmLoop(cats, width)
	return true
}

func (s *Server) prewarmLoop(cats []string, width int) {
	p := s.prewarm
	countErr := func() {
		p.mu.Lock()
		p.errors++
		p.mu.Unlock()
	}
	// waitIdle 在请求/内存忙时退让：最长等 maxWait，期间一旦空闲立即返回。
	// 返回 false 表示超时或任务被取消。
	waitIdle := func(maxWait time.Duration) bool {
		deadline := time.Now().Add(maxWait)
		for {
			if s.prewarmCancelled() {
				return false
			}
			if s.prewarmYield() {
				return true
			}
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	for _, cat := range cats {
		p.mu.Lock()
		p.current = cat
		p.mu.Unlock()

		if s.prewarmCancelled() {
			break
		}
		// 图库页：只在渲染空闲时占用名额；长时间有请求就跳过该分类。
		if waitIdle(5 * time.Minute) {
			for attempt := 0; attempt < 3; attempt++ {
				err := s.prewarmGallery(cat)
				if err == nil {
					break
				}
				if errors.Is(err, errPrewarmBusy) {
					time.Sleep(time.Second) // 有请求在渲染，让路后重试
					continue
				}
				countErr()
				break
			}
		} else {
			countErr()
		}

		// 缩略图：逐张做，每张之前都看一眼请求是否来了。
		if width > 0 {
			rows, err := s.st.Links(context.Background(), cat)
			if err == nil {
				for _, row := range rows {
					if s.prewarmCancelled() {
						break
					}
					// 有请求就等（最多 5 分钟），等到空闲再继续，而不是放弃。
					if !waitIdle(5 * time.Minute) {
						if s.prewarmCancelled() {
							break
						}
						countErr()
						break
					}
					if e := s.prewarmThumb(cat, row.ID, width); e != nil {
						if errors.Is(e, errPrewarmBusy) {
							time.Sleep(time.Second) // 预算被请求占着：退让
							if e2 := s.prewarmThumb(cat, row.ID, width); e2 != nil {
								countErr()
							}
							continue
						}
						countErr()
					}
					// 每张之间小睡，避免和线上抢 CPU
					time.Sleep(20 * time.Millisecond)
				}
			}
		}
		p.mu.Lock()
		p.done++
		p.mu.Unlock()
	}
	p.mu.Lock()
	p.running = false
	if p.cancel {
		p.message = "已取消"
	} else {
		p.message = "完成"
	}
	p.mu.Unlock()
}

// prewarmCancelled 任务是否被管理员取消。
func (s *Server) prewarmCancelled() bool {
	p := s.prewarm
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cancel
}

// handleAdminCacheCancel 取消正在运行的预热任务。
func (s *Server) handleAdminCacheCancel(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	p := s.prewarm
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		s.adminResult(w, "当前没有运行中的预热任务")
		return
	}
	p.cancel = true
	p.mu.Unlock()
	s.adminResult(w, "已请求取消预热，当前项完成后停止。")
}

// prewarmYield 判断当前是否适合做预热：
//   - 最近 1 秒内有请求 → 让路；
//   - 内存压力已进入 warn 档 → 让路；
//   - 有请求正在渲染图库 → 让路。
//
// 返回 true 表示可以继续；false 表示应暂时停手。
func (s *Server) prewarmYield() bool {
	// 启动/刷新扫描优先级最高：扫描期间预热完全让路。
	if s.st.Scanning() {
		return false
	}
	if s.mem.RecentlyActive(time.Second) {
		return false
	}
	if s.mem.Pressure() > 0.3 {
		return false
	}
	return s.galleryGate.idleForPrewarm()
}

// prewarmGallery 渲染并落盘一个分类的图库页（低优先级：仅在渲染空闲时占用名额）。
func (s *Server) prewarmGallery(cat string) error {
	hidden := s.galleryHiddenSet()
	all := s.sortedCategories(context.Background())
	cats := make([]string, 0, len(all))
	for _, c := range all {
		if !hidden[c] {
			cats = append(cats, c)
		}
	}
	for i := 0; i < 3; i++ {
		if !s.galleryGate.tryAcquireWhenIdle() {
			return errPrewarmBusy // 有请求在渲染：让路，稍后再来
		}
		_, err := s.buildGalleryPage(context.Background(), cat, cats)
		s.galleryGate.release()
		if err == nil {
			return nil
		}
		if errors.Is(err, errGalleryBusy) {
			time.Sleep(time.Duration(i+1) * 300 * time.Millisecond)
			continue
		}
		return err
	}
	return errPrewarmBusy
}

// prewarmThumb 生成一张缩略图（已存在则跳过）。
func (s *Server) prewarmThumb(category, file string, width int) error {
	path := filepath.Join(s.dataDir, filepath.FromSlash(category), file)
	_, cachePath := s.previewCachePath(category, file, width)
	if fi, err := os.Stat(cachePath); err == nil && fi.Size() > 0 {
		return nil
	}
	cfg, err := imageConfig(path)
	if err != nil {
		return err
	}
	units := previewUnits(cfg.Width, cfg.Height)
	if s.mem.Permits() == 0 || units > s.mem.ScaleMP(s.cfg.PreviewMP) {
		return errPrewarmBusy
	}
	// 低优先级：最多占用一半像素预算，另一半留给线上请求。
	if !s.previewGate.tryAcquireLow(units, 0.5) {
		return errPrewarmBusy
	}
	defer s.previewGate.release(units)
	data, _, err := compressPreview(path, width)
	if err != nil || len(data) == 0 {
		return fmt.Errorf("compress %s: %w", file, err)
	}
	return s.writePreviewFile(cachePath, data)
}

// writePreviewFile 原子写缩略图缓存。
func (s *Server) writePreviewFile(cachePath string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	tmp := cachePath + "." + strconv.FormatInt(time.Now().UnixNano(), 36) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, cachePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ---------------------------------------------------------------- 后台处理器

// handleAdminCacheClear 清理全部缓存（缩略图 + 图库 HTML）。
func (s *Server) handleAdminCacheClear(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	before, err := s.clearCaches()
	if err != nil {
		s.adminResult(w, "清理失败: "+err.Error())
		return
	}
	s.adminResult(w, fmt.Sprintf("缓存已清理：图库页 %d 个（%.1f MB）、缩略图 %d 个（%.1f MB）",
		before.GalleryFiles, float64(before.GalleryBytes)/1048576,
		before.ThumbFiles, float64(before.ThumbBytes)/1048576))
}

// handleAdminCachePrewarm 启动预热：图库页 + 可选缩略图。
func (s *Server) handleAdminCachePrewarm(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	category := strings.TrimSpace(r.PostFormValue("category"))
	width, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("width")))
	if width != 0 && (width < previewMinWidth || width > previewMaxWidth) {
		s.adminResult(w, fmt.Sprintf("缩略图宽度需在 %d~%d 之间（0 表示只预热图库页）", previewMinWidth, previewMaxWidth))
		return
	}

	var cats []string
	if category != "" {
		if !s.validCategory(category) {
			s.adminResult(w, "分类不合法")
			return
		}
		if _, err := s.st.CountWith(r.Context(), category, store.OrientationAny); err != nil {
			s.adminResult(w, "分类不存在")
			return
		}
		cats = []string{category}
	} else {
		hidden := s.galleryHiddenSet()
		for _, c := range s.sortedCategories(r.Context()) {
			if !hidden[c] {
				cats = append(cats, c)
			}
		}
	}
	if len(cats) == 0 {
		s.adminResult(w, "没有可预热的分类")
		return
	}
	if !s.startPrewarm(cats, width) {
		s.adminResult(w, "已有预热任务在运行，请稍后查看进度")
		return
	}
	s.adminResult(w, fmt.Sprintf("预热已开始：%d 个分类，缩略图宽度 %d（0 表示只渲染图库页），进度见本页", len(cats), width))
}

// handleAdminCacheStatus 返回预热进度与缓存占用（JSON）。
func (s *Server) handleAdminCacheStatus(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{
		"prewarm": s.prewarmSnapshot(),
		"cache":   s.cacheStats(),
	})
}
