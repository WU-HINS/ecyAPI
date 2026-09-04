package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sort"
	"time"
)

// homeCard 主页分类卡片。
type homeCard struct {
	Path, Label, Desc, Kind string
	PreviewURL              string
	Extra                   []string
}

// renderHome 渲染 API 发布主页。
func (s *Server) renderHome(w http.ResponseWriter, r *http.Request) {
	cards := make([]homeCard, 0, len(endpoints))
	for _, ep := range endpoints {
		if ep.path == "aimp" {
			continue // 与 ai 自适应重复，主页不单列
		}
		c := homeCard{
			Path:       ep.path,
			Label:      ep.label,
			Desc:       ep.desc,
			Kind:       ep.kind,
			PreviewURL: "/" + ep.path,
		}
		if ep.path == "acg" {
			c.Desc = "测试项目，还在不断修正"
		}
		cards = append(cards, c)
	}
	type homeData struct {
		Title string
		Cards []homeCard
		Year  int
	}
	s.render(w, http.StatusOK, "home.html", homeData{Title: "栗次元API - 举个栗子", Cards: cards, Year: time.Now().Year()})
}

// handleDocs 渲染 JSON 参数文档页。
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	cats := s.sortedCategories(r.Context())
	s.render(w, http.StatusOK, "docs.html", map[string]any{"Categories": cats})
}

// galleryImage 图库页图片。
type galleryImage struct {
	URL   string
	Thumb string
}

// handleGallery 渲染图库页。
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	cats := s.sortedCategories(r.Context())
	if len(cats) == 0 {
		s.render(w, http.StatusInternalServerError, "gallery.html", map[string]any{"LoadError": true})
		return
	}
	current := r.URL.Query().Get("category")
	counts := s.categoryCountMap(r.Context())
	if !contains(cats, current) {
		current = cats[0]
	}
	rows, err := s.st.Links(r.Context(), current)
	if err != nil {
		rows = nil
	}
	// 打乱顺序
	if len(rows) > 1 {
		for i := len(rows) - 1; i > 0; i-- {
			j := int(time.Now().UnixNano()) % (i + 1)
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	images := make([]galleryImage, 0, len(rows))
	for _, row := range rows {
		images = append(images, galleryImage{URL: row.Link, Thumb: row.Link})
	}
	data := map[string]any{
		"Current":   current,
		"Images":    images,
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

func (s *Server) sessionStart(w http.ResponseWriter) {
	token := newSessionToken()
	s.sessions.Store(token, time.Now().Add(24*time.Hour))
	http.SetCookie(w, &http.Cookie{
		Name:     "ecy_admin",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})
}

func (s *Server) sessionEnd(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("ecy_admin"); err == nil {
		s.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "ecy_admin", Value: "", Path: "/", MaxAge: -1})
}

var _ = sort.Strings
