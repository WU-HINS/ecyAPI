package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ecyapi/internal/store"
)

// homeCard 主页分类卡片。
type homeCard struct {
	Path, Label, Desc, Kind string
	PreviewURL              string
	URL                     string // 展示用的完整接口地址（含对外域名）
	Extra                   []string
}

// renderHome 渲染 API 发布主页。
func (s *Server) renderHome(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	endpoints := append([]endpoint(nil), s.endpoints...)
	s.mu.RUnlock()
	// 首页只展示「一级分类」：/P/ii 这类深层路径归一到 /P，
	// 同一级下的多个栏目合并成一张卡片（点进去 /P 会由父聚合随机出图）。
	// 图库页 /img 仍按完整路径列分类，不受影响。
	cards := make([]homeCard, 0, len(endpoints))
	seenTop := make(map[string]bool, len(endpoints))
	for _, ep := range endpoints {
		top := topSegment(ep.path)
		if top == "" || seenTop[top] {
			continue
		}
		seenTop[top] = true
		// 深层栏目：卡片路径/标题都用一级分类名，描述沿用第一个栏目
		path, label := ep.path, ep.label
		if top != ep.path {
			path, label = top, top
		}
		cards = append(cards, homeCard{
			Path:       path,
			Label:      label,
			Desc:       ep.desc,
			Kind:       ep.kind,
			PreviewURL: "/" + path,
			URL:        s.absoluteURL(r, "/"+path),
		})
	}
	type homeData struct {
		Title string
		Cards []homeCard
		// HeroImage 首页横幅背景（取第一个栏目/分类的预览图，没有就留空用纯色）
		HeroImage string
		// StaticBase 看板娘资源的根路径（带版本号，可长缓存）
		StaticBase string
		Year       int
		Slogan     string
		Share      string
		ICP        string
		ICPURL     string
		Logs       []string
	}
	s.mu.RLock()
	slogan, share, icp, icpURL := s.slogan, s.share, s.icp, s.icpURL
	logs := append([]string(nil), s.logs...)
	s.mu.RUnlock()
	s.render(w, http.StatusOK, "home.html", homeData{
		Title:      "栗次元API - 举个栗子",
		Cards:      s.orderCards(append(cards, s.homeGroups(r.Context(), r)...)),
		HeroImage:  heroImage(cards, s.sortedCategories(r.Context())),
		StaticBase: "/@index/v" + s.staticVersion,
		Year:       time.Now().Year(),
		Slogan:     slogan,
		Share:      share,
		ICP:        icp,
		ICPURL:     icpURL,
		Logs:       logs,
	})
}

// homeGroups 把虚拟分组做成「和普通分类一样的卡片」：
// 预览图直接指向 /@img/<name>?w=480（每次加载都会 302 到组内随机一张），
// 所以页面上的「点击图片刷新」天然就是重新随机，而不是刷新同一张图。
func (s *Server) homeGroups(ctx context.Context, r *http.Request) []homeCard {
	snap := s.groupsSnapshot()
	if len(snap.list) == 0 {
		return nil
	}
	out := make([]homeCard, 0, len(snap.list))
	for _, g := range snap.list {
		if n, err := s.st.GroupCount(ctx, g.Categories, store.OrientationAny); err != nil || n == 0 {
			continue // 组里一张可用图都没有就不展示
		}
		path := "/@img/" + g.Name
		out = append(out, homeCard{
			Path:       path,
			Label:      g.Label,
			Desc:       g.Desc,
			Kind:       "image",
			PreviewURL: path,
			URL:        s.absoluteURL(r, path),
		})
	}
	return out
}

// topSegment 取路径的第一段（一级分类）："/P/ii" → "P"，"P" → "P"。
func topSegment(path string) string {
	path = strings.Trim(path, "/")
	if path == "" {
		return ""
	}
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return path
}

// heroImage 取第一个可用栏目作为首页横幅背景；没有栏目时退回第一个分类。
// 之前这里写死了 /fj，部署到没有该分类的实例上会一直 404。
func heroImage(cards []homeCard, cats []string) string {
	for _, c := range cards {
		if c.PreviewURL != "" {
			return c.PreviewURL
		}
	}
	if len(cats) > 0 {
		return "/" + cats[0]
	}
	return ""
}

// handleDocs 渲染 JSON 参数文档页。
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	cats := s.sortedCategories(r.Context())
	s.mu.RLock()
	endpoints := append([]endpoint(nil), s.endpoints...)
	s.mu.RUnlock()
	s.render(w, http.StatusOK, "docs.html", map[string]any{"Categories": cats, "Endpoints": endpoints})
}

// galleryImage 图库页图片。Thumb 走压缩预览（?w=480），
// 点击放大查看时才加载原图 URL。
type galleryImage struct {
	URL   string
	Thumb string
}

// handleGallery 渲染图库页。后台设置为隐藏的分类不展示、不可访问。
//
// 图库要「展示全部」：单次渲染 5 万张约 2 秒、8MB，100 并发不可能各自渲染。
// 因此渲染结果按「索引代次 + 隐藏集合 + 抽样上限」落盘缓存：
//   - 命中：直接 sendfile，不占渲染名额，100 并发只是带宽问题；
//   - 未命中：一个请求渲染，其余等待缓存出现（最多 ECY_GALLERY_WAIT），超时才 503。
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	hidden := s.galleryHiddenSet()
	all := s.sortedCategories(r.Context())
	cats := make([]string, 0, len(all))
	for _, c := range all {
		if !hidden[c] {
			cats = append(cats, c)
		}
	}
	if len(cats) == 0 {
		s.render(w, http.StatusInternalServerError, "gallery.html", map[string]any{"LoadError": true})
		return
	}
	current := r.URL.Query().Get("category")
	if !contains(cats, current) {
		current = cats[0]
	}

	// 1) 缓存命中：直接输出。
	gen := s.st.Generation()
	dir, name := s.galleryCacheKey(current, gen)
	cachePath := filepath.Join(s.galleryCacheRoot(), dir, name)
	if s.serveGalleryCache(w, r, cachePath) {
		return
	}

	// 2) 未命中：内存越接近红线，控制越严（p≥0.9 直接拒绝新渲染）。
	if s.mem.Permits() == 0 {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "服务繁忙（内存压力），请稍后重试", http.StatusServiceUnavailable)
		return
	}
	// 名额争用时不排长队：要么拿到名额自己渲染，要么等别人渲染完直接命中缓存。
	// 这样冷启动瞬间的 100 并发只会产生「2 个渲染 + 98 个等待命中」，而不是 98 个 503。
	waitDur := time.Duration(s.cfg.GalleryWaitSeconds) * time.Second
	deadline := time.Now().Add(waitDur)
	acquired := s.galleryGate.tryAcquire()
	for !acquired && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if s.serveGalleryCache(w, r, cachePath) {
			return
		}
		acquired = s.galleryGate.tryAcquire()
	}
	if !acquired {
		w.Header().Set("Retry-After", strconv.Itoa(s.cfg.GalleryWaitSeconds))
		http.Error(w, "图库正在渲染大分类，请稍后重试", http.StatusServiceUnavailable)
		return
	}
	defer s.galleryGate.release()

	// 拿到名额后再查一次：排队期间可能已被上一个渲染者填好。
	if s.serveGalleryCache(w, r, cachePath) {
		return
	}

	// 3) 真正渲染（与后台预热共用同一段逻辑）。
	body, err := s.buildGalleryPage(r.Context(), current, cats)
	if err != nil {
		if errors.Is(err, errGalleryBusy) {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "服务繁忙（内存压力），请稍后重试", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// buildGalleryPage 渲染并落盘指定分类的图库页（供请求路径与后台预热共用）。
func (s *Server) buildGalleryPage(ctx context.Context, current string, cats []string) ([]byte, error) {
	counts := s.categoryCountMap(ctx)
	total, err := s.st.CountWith(ctx, current, store.OrientationAny)
	if err != nil {
		total = 0
	}
	if !s.mem.Allow(int64(total) * 400) {
		return nil, errGalleryBusy
	}
	var rows []store.Row
	if s.cfg.GalleryMax > 0 && total > s.cfg.GalleryMax {
		rows, err = s.st.RandomWith(ctx, current, s.cfg.GalleryMax, store.OrientationAny)
	} else {
		rows, err = s.st.Links(ctx, current)
	}
	if err != nil {
		rows = nil
	}
	images := make([]galleryImage, 0, len(rows))
	for _, row := range rows {
		images = append(images, galleryImage{URL: row.Link, Thumb: row.Link + "?w=480"})
	}
	type cat struct {
		Name  string
		Count int
	}
	catList := make([]cat, 0, len(cats))
	for _, c := range cats {
		catList = append(catList, cat{Name: c, Count: counts[c]})
	}
	data := map[string]any{
		"Current":    current,
		"Images":     images,
		"Total":      total,
		"Shown":      len(images),
		"Sampled":    total > len(images),
		"Categories": catList,
		"LoadError":  false,
	}
	body, err := s.renderGalleryHTML(data)
	if err != nil {
		return nil, err
	}
	dir, name := s.galleryCacheKey(current, s.st.Generation())
	s.writeGalleryCache(filepath.Join(s.galleryCacheRoot(), dir, name), body)
	return body, nil
}

// galleryHiddenList 返回隐藏分类的稳定副本（用于缓存键）。
func (s *Server) galleryHiddenList() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.galleryHidden))
	copy(out, s.galleryHidden)
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// writeRowsJSON 以 JSON 输出图片列表（后台预览用）。
func writeRowsJSON(w http.ResponseWriter, rows []store.Row) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(rows)
}

// newSessionToken 生成会话令牌。
func newSessionToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) sessionValid(r *http.Request) bool {
	c, err := r.Cookie("ecy_admin")
	if err != nil || c.Value == "" {
		return false
	}
	v, ok := s.sessions.Load(c.Value)
	if !ok {
		return false
	}
	exp := v.(time.Time)
	if time.Now().After(exp) {
		s.sessions.Delete(c.Value)
		return false
	}
	return true
}

func (s *Server) sessionStart(w http.ResponseWriter, r *http.Request) {
	token := newSessionToken()
	s.sessions.Store(token, time.Now().Add(24*time.Hour))
	http.SetCookie(w, &http.Cookie{
		Name:     "ecy_admin",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})
}

// requestIsHTTPS 判断外层是否为 HTTPS（直连 TLS 或反代声明），
// 只有确定是 HTTPS 时才给会话 Cookie 加 Secure，避免纯 HTTP 部署下登录失效。
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// sweepSessionsLoop 定期清理过期会话，避免 sync.Map 只增不减。
func (s *Server) sweepSessionsLoop() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.sweepSessions()
	}
}

// sweepSessions 删除已过期的会话令牌。
func (s *Server) sweepSessions() {
	now := time.Now()
	s.sessions.Range(func(k, v any) bool {
		if exp, ok := v.(time.Time); ok && now.After(exp) {
			s.sessions.Delete(k)
		}
		return true
	})
}

func (s *Server) sessionEnd(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("ecy_admin"); err == nil {
		s.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "ecy_admin", Value: "", Path: "/", MaxAge: -1})
}
