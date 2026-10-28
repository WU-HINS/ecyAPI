package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
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
	cards := make([]homeCard, 0, len(endpoints))
	for _, ep := range endpoints {
		cards = append(cards, homeCard{
			Path:       ep.path,
			Label:      ep.label,
			Desc:       ep.desc,
			Kind:       ep.kind,
			PreviewURL: "/" + ep.path,
			URL:        s.absoluteURL(r, "/"+ep.path),
		})
	}
	type homeData struct {
		Title  string
		Cards  []homeCard
		Year   int
		Slogan string
		Share  string
		ICP    string
		ICPURL string
		Logs   []string
	}
	s.mu.RLock()
	slogan, share, icp, icpURL := s.slogan, s.share, s.icp, s.icpURL
	logs := append([]string(nil), s.logs...)
	s.mu.RUnlock()
	s.render(w, http.StatusOK, "home.html", homeData{
		Title:  "栗次元API - 举个栗子",
		Cards:  cards,
		Year:   time.Now().Year(),
		Slogan: slogan,
		Share:  share,
		ICP:    icp,
		ICPURL: icpURL,
		Logs:   logs,
	})
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
	counts := s.categoryCountMap(r.Context())
	if !contains(cats, current) {
		current = cats[0]
	}
	// 图库是唯一「与图片数成正比」的页面：必须抽样，不能把整个分类搬进内存。
	// 大分类（5 万张）全量渲染单请求就要 ~10MB，几十并发直接 OOM。
	total, err := s.st.CountWith(r.Context(), current, store.OrientationAny)
	if err != nil {
		total = 0
	}
	limit := s.cfg.GalleryMax
	if limit > total {
		limit = total
	}
	rows, err := s.st.RandomWith(r.Context(), current, limit, store.OrientationAny)
	if err != nil {
		rows = nil
	}
	images := make([]galleryImage, 0, len(rows))
	for _, row := range rows {
		images = append(images, galleryImage{URL: row.Link, Thumb: row.Link + "?w=480"})
	}
	data := map[string]any{
		"Current":   current,
		"Images":    images,
		"Total":     total,
		"Shown":     len(images),
		"Sampled":   total > len(images),
		"LoadError": false,
	}
	type cat struct {
		Name  string
		Count int
	}
	catList := make([]cat, 0, len(cats))
	for _, name := range cats {
		catList = append(catList, cat{Name: name, Count: counts[name]})
	}
	data["Categories"] = catList
	s.render(w, http.StatusOK, "gallery.html", data)
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
