package server

// web.go —— M4：云端命名空间 /@web/*
//
// 与本地完全隔离：
//   - 云端单独一份紧凑索引（FlagRemote），分类互不影响
//   - /@web/{分类}       → 302 到本域 /@web/pic/{分类}/{文件}（不暴露后端地址）
//   - /@web/pic/...      → 代理流式转发（io.CopyN，不落盘）
//   - /@web/json?{分类}  → 云端 JSON；/@web → 全部云端分类随机

import (
	"bytes"
	"image"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"

	"ecyapi/internal/index"
	"ecyapi/internal/remote"
)

type webCat struct {
	cat  index.Category
	all  []uint32
	land []uint32
	port []uint32
}

type webBackend struct {
	ix   *index.Index
	src  remote.Source
	mu   map[string]*webCat
	cap  int
	keys []string
}

func newWebBackend(ix *index.Index, src remote.Source) *webBackend {
	return &webBackend{ix: ix, src: src, mu: map[string]*webCat{}, cap: 128}
}

func (b *webBackend) get(name string) (*webCat, bool) {
	if c, ok := b.mu[name]; ok {
		return c, true
	}
	cat, ok := b.ix.Find(name)
	if !ok {
		return nil, false
	}
	c := &webCat{cat: cat}
	for i := 0; i < cat.Count; i++ {
		rec, ok := b.ix.At(cat, i)
		if !ok {
			continue
		}
		c.all = append(c.all, uint32(i))
		switch rec.Flags & 0x3 {
		case index.OriPort:
			c.port = append(c.port, uint32(i))
		default:
			c.land = append(c.land, uint32(i))
			c.port = append(c.port, uint32(i))
		}
	}
	if len(b.keys) >= b.cap {
		delete(b.mu, b.keys[0])
		b.keys = b.keys[1:]
	}
	b.mu[name] = c
	b.keys = append(b.keys, name)
	return c, true
}

func (c *webCat) positions(want string) []uint32 {
	switch want {
	case "landscape":
		return c.land
	case "portrait":
		return c.port
	}
	return c.all
}

// row 生成对外链接（始终是本域 /@web/pic/...）。
func (c *webCat) row(b *webBackend, pos uint32) (string, bool) {
	rec, ok := b.ix.At(c.cat, int(pos))
	if !ok {
		return "", false
	}
	return "/@web/pic/" + c.cat.Name + "/" + rec.Name, true
}

// webRoute 处理所有 /@web 下的请求。
func (s *Server) webRoute(w http.ResponseWriter, r *http.Request) {
	if s.web == nil {
		plainError(w, "云端后端未配置。", http.StatusNotFound)
		return
	}
	p := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/@web"), "/")

	if strings.HasPrefix(p, "pic/") {
		s.webServeFile(w, r, strings.TrimPrefix(p, "pic/"))
		return
	}
	if strings.HasPrefix(p, "json") {
		s.webJSON(w, r)
		return
	}
	if p == "" {
		s.webRandomAll(w, r)
		return
	}
	if !strings.Contains(p, "/") {
		s.webRandom(w, r, p)
		return
	}
	http.NotFound(w, r)
}

// webServeFile 代理流式输出云端原图（不落盘、不暴露后端地址）。
func (s *Server) webServeFile(w http.ResponseWriter, r *http.Request, rest string) {
	i := strings.LastIndex(rest, "/")
	if i <= 0 {
		http.NotFound(w, r)
		return
	}
	cat, name := rest[:i], rest[i+1:]
	cc, ok := s.web.get(cat)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.web.ix.Lookup(cc.cat, name); !ok {
		http.NotFound(w, r)
		return
	}
	rc, err := s.web.src.Open(r.Context(), cat+"/"+name)
	if err != nil {
		plainError(w, "云端读取失败。", http.StatusBadGateway)
		return
	}
	defer rc.Close()

	// 惰性方向判定：只读文件头（≤64KB），判定成功后把 1 字节写回索引，
	// 下次请求就能命中方向过滤，不再重复解码。
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(rc, head)
	head = head[:n]
	s.learnOrientation(s.web, cc.cat, name, head)

	w.Header().Set("Cache-Control", "public, max-age=604800, stale-while-revalidate=86400")
	if ct := webContentType(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if len(head) > 0 {
		if _, err := w.Write(head); err != nil {
			return
		}
	}
	_, _ = io.Copy(w, rc)
}

func (s *Server) webPick(w http.ResponseWriter, r *http.Request, cat string) bool {
	cc, ok := s.web.get(cat)
	if !ok {
		return false
	}
	want := "any"
	if o := r.URL.Query().Get("orientation"); o != "" {
		want = o
	} else if !isMobileUA(r) {
		want = "landscape"
	}
	pos := cc.positions(want)
	if len(pos) == 0 {
		pos = cc.all
	}
	if len(pos) == 0 {
		return false
	}
	link, ok := cc.row(s.web, pos[rand.IntN(len(pos))])
	if !ok {
		return false
	}
	if _, hasJSON := r.URL.Query()["json"]; hasJSON {
		plainText(w, s.absoluteURL(r, link))
		return true
	}
	redirectRandom(w, r, link) // 目标始终是本域 /@web/pic/...
	return true
}

func (s *Server) webRandom(w http.ResponseWriter, r *http.Request, cat string) {
	if s.webPick(w, r, cat) {
		return
	}
	plainError(w, "No links found.", http.StatusNotFound)
}

func (s *Server) webRandomAll(w http.ResponseWriter, r *http.Request) {
	cats := s.web.ix.Categories()
	if len(cats) == 0 {
		plainError(w, "No links found.", http.StatusNotFound)
		return
	}
	for i := 0; i < 8; i++ {
		if s.webPick(w, r, cats[rand.IntN(len(cats))].Name) {
			return
		}
	}
	plainError(w, "No links found.", http.StatusNotFound)
}

func (s *Server) webJSON(w http.ResponseWriter, r *http.Request) {
	var cat string
	for k := range r.URL.Query() {
		switch k {
		case "json", "quantity", "orientation", "w":
			continue
		}
		cat = k
		break
	}
	if cat == "" {
		s.webRandomAll(w, r)
		return
	}
	if s.webPick(w, r, cat) {
		return
	}
	plainError(w, "No such category.", http.StatusNotFound)
}

func webContentType(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".png"):
		return "image/png"
	case strings.HasSuffix(l, ".jpg"), strings.HasSuffix(l, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(l, ".webp"):
		return "image/webp"
	case strings.HasSuffix(l, ".gif"):
		return "image/gif"
	case strings.HasSuffix(l, ".avif"):
		return "image/avif"
	case strings.HasSuffix(l, ".mp4"):
		return "video/mp4"
	case strings.HasSuffix(l, ".webm"):
		return "video/webm"
	}
	return ""
}

// isMobileUA 简易移动端判定（仅用于云端链路的方向自动选择）。
func isMobileUA(r *http.Request) bool {
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	return strings.Contains(ua, "mobile") || strings.Contains(ua, "android") || strings.Contains(ua, "iphone")
}

// learnOrientation 从文件头判定方向并回写索引（惰性 + 持久化）。
func (s *Server) learnOrientation(b *webBackend, cat index.Category, name string, head []byte) {
	rec, pos, ok := b.ix.LookupPos(cat, name)
	if !ok {
		return
	}
	if rec.Flags&0x3 != index.OriUnknown {
		return // 已经判定过
	}
	if len(head) < 32 {
		return
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(head))
	if err != nil {
		return
	}
	flags := rec.Flags &^ 0x3
	switch {
	case cfg.Width > cfg.Height:
		flags |= index.OriLand
	case cfg.Height > cfg.Width:
		flags |= index.OriPort
	default:
		flags |= index.OriLand // 方图按横图处理
	}
	if err := b.ix.SetFlags(cat, pos, flags); err != nil {
		return
	}
	b.invalidate(cat.Name) // 方向变了，缓存的位置数组要重建
}

// invalidate 丢弃某分类的缓存（方向回写后调用）。
func (b *webBackend) invalidate(name string) {
	delete(b.mu, name)
	for i, k := range b.keys {
		if k == name {
			b.keys = append(b.keys[:i], b.keys[i+1:]...)
			break
		}
	}
}
