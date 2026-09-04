// Package server 组装 HTTP 路由与处理器。
package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ecyapi/internal/config"
	"ecyapi/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// endpoints 定义分类路径与桌面/移动端数据表映射。
type endpoint struct {
	path   string
	desk   string
	mobile string // 为空表示不自适应
	label  string
	desc   string
	kind   string // image / video
}

// endpointsFile 双端自适应绑定配置，存于数据目录外。
const endpointsFile = "data/endpoints.json"

// settingsFile 站点设置（对外域名等），存于数据目录外。
const settingsFile = "data/settings.json"

var defaultEndpoints = []endpoint{
	{"ycy", "pc", "mp", "二次元自适应", "自适应设备，桌面横图、移动竖图", "image"},
	{"moez", "moe", "moemp", "萌版自适应", "萌版图片，自适应设备", "image"},
	{"ysz", "ys", "ysmp", "原神自适应", "原神图片，自适应设备", "image"},
	{"pc", "pc", "", "PC横图", "桌面端横版壁纸", "image"},
	{"mp", "mp", "", "移动竖图", "手机竖版壁纸", "image"},
	{"moe", "moe", "", "萌版横图", "萌系横版壁纸", "image"},
	{"moemp", "moemp", "", "萌版竖图", "萌系竖版壁纸", "image"},
	{"ai", "ai", "aimp", "AI自适应", "AI 画图，自适应设备", "image"},
	{"aimp", "aimp", "", "AI竖图", "AI 竖版壁纸", "image"},
	{"ys", "ys", "", "原神横图", "原神横版壁纸", "image"},
	{"ysmp", "ysmp", "", "原神竖图", "原神竖版壁纸", "image"},
	{"fj", "fj", "", "风景横图", "风景横版壁纸", "image"},
	{"bd", "bd", "", "白底横图", "白底素材图", "image"},
	{"tx", "tx", "", "头像方图", "方形头像图", "image"},
	{"lai", "lai", "", "七濑胡桃", "七濑胡桃表情包", "image"},
	{"xhl", "xhl", "", "小狐狸", "小狐狸图包", "image"},
	{"acg", "acg", "", "ACG动图", "随机 mp4 动图", "video"},
}

var mobileRe = regexp.MustCompile(`(?i)mobile|android|iphone|ipad|ipod|touch|samsung|windows phone`)

// Server HTTP 服务。
type Server struct {
	cfg       config.Config
	st        store.Store
	admin     store.Admin
	tpl       *template.Template
	sessions  sync.Map // token -> expiry
	mu        sync.RWMutex
	endpoints []endpoint
	dataDir   string
	baseURL   string // 对外展示的 API 域名（含协议），留空按请求推断
}

// New 创建服务，使用本地文件数据源。
func New(cfg config.Config) (*Server, error) {
	st, err := store.NewFileStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	admin, _ := store.NewFileAdmin(st)
	s := &Server{cfg: cfg, st: st, admin: admin, dataDir: store.Dir(st)}
	if err := s.loadEndpoints(); err != nil {
		return nil, err
	}
	s.baseURL = cfg.BaseURL
	if err := s.loadSettings(); err != nil {
		return nil, err
	}
	funcMap := template.FuncMap{
		"Add": func(a, b int) int { return a + b },
	}
	tpl, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s.tpl = tpl
	return s, nil
}

// ---------------------------------------------------------------- 双端绑定持久化

type endpointJSON struct {
	Path   string `json:"path"`
	Desk   string `json:"desk"`
	Mobile string `json:"mobile"`
	Label  string `json:"label"`
	Desc   string `json:"desc"`
	Kind   string `json:"kind"`
}

func (s *Server) loadEndpoints() error {
	s.endpoints = append([]endpoint(nil), defaultEndpoints...)
	data, err := os.ReadFile(endpointsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var customs []endpointJSON
	if err := json.Unmarshal(data, &customs); err != nil {
		return fmt.Errorf("parse %s: %w", endpointsFile, err)
	}
	for _, c := range customs {
		if store.IsValidIdentifier(c.Path) && store.IsValidIdentifier(c.Desk) && store.IsValidIdentifier(c.Mobile) {
			s.endpoints = append(s.endpoints, endpoint{c.Path, c.Desk, c.Mobile, c.Label, c.Desc, "image"})
		}
	}
	return nil
}

func (s *Server) saveEndpoints() error {
	s.mu.RLock()
	var customs []endpointJSON
	for _, ep := range s.endpoints {
		if !isDefaultEndpoint(ep.path) {
			customs = append(customs, endpointJSON{ep.path, ep.desk, ep.mobile, ep.label, ep.desc, ep.kind})
		}
	}
	s.mu.RUnlock()
	data, err := json.MarshalIndent(customs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(endpointsFile, data, 0o644)
}

// ---------------------------------------------------------------- 站点设置持久化

type settingsJSON struct {
	BaseURL string `json:"base_url"`
}

// loadSettings 读取站点设置；文件中已保存的域名优先于环境变量。
func (s *Server) loadSettings() error {
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var cfg settingsJSON
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse %s: %w", settingsFile, err)
	}
	if v := normalizeBaseURL(cfg.BaseURL); v != "" {
		s.baseURL = v
	}
	return nil
}

func (s *Server) saveSettings() error {
	s.mu.RLock()
	cfg := settingsJSON{BaseURL: s.baseURL}
	s.mu.RUnlock()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsFile, data, 0o644)
}

// normalizeBaseURL 规整域名：补协议、去尾部斜杠；非法输入返回空串。
func normalizeBaseURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		v = "https://" + v
	}
	v = strings.TrimRight(v, "/")
	// 基本合法性：协议 + 非空主机，且不含空格/路径以外的奇怪字符
	rest := strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://")
	if rest == "" || strings.ContainsAny(rest, " \t\r\n") {
		return ""
	}
	return v
}

// setBaseURL 更新对外域名并持久化；传空串表示恢复按请求推断。
func (s *Server) setBaseURL(v string) error {
	s.mu.Lock()
	s.baseURL = normalizeBaseURL(v)
	s.mu.Unlock()
	return s.saveSettings()
}

func isDefaultEndpoint(path string) bool {
	for _, ep := range defaultEndpoints {
		if ep.path == path {
			return true
		}
	}
	return false
}

// addBinding 新增双端自适应绑定栏目。
func (s *Server) addBinding(path, desk, mobile, label, desc string) error {
	if !store.IsValidIdentifier(path) {
		return fmt.Errorf("栏目路径不合法")
	}
	if !store.IsValidIdentifier(desk) || !store.IsValidIdentifier(mobile) {
		return fmt.Errorf("分类名不合法")
	}
	s.mu.Lock()
	for _, ep := range s.endpoints {
		if ep.path == path {
			s.mu.Unlock()
			return fmt.Errorf("栏目 %s 已存在", path)
		}
	}
	s.endpoints = append(s.endpoints, endpoint{path, desk, mobile, label, desc, "image"})
	s.mu.Unlock()
	return s.saveEndpoints()
}

// Handler 返回路由根处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("GET /docs", s.handleDocs)
	mux.HandleFunc("GET /img", s.handleGallery)
	mux.HandleFunc("GET /json", s.handleJSON)
	mux.HandleFunc("GET /pic/{category}/{file}", s.handlePic)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
	mux.HandleFunc("GET /admin", s.handleAdmin)
	mux.HandleFunc("POST /admin", s.handleAdmin)
	mux.HandleFunc("POST /admin/update", s.handleAdminUpdate)
	mux.HandleFunc("POST /admin/create", s.handleAdminCreate)
	mux.HandleFunc("POST /admin/upload", s.handleAdminUpload)
	mux.HandleFunc("POST /admin/delete", s.handleAdminDelete)
	mux.HandleFunc("POST /admin/binding", s.handleAdminBinding)
	mux.HandleFunc("POST /admin/settings", s.handleAdminSettings)
	mux.HandleFunc("GET /admin/data", s.handleAdminData)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	return logMiddleware(mux)
}

// handlePic 提供本地图片访问：/pic/{分类}/{文件名}
func (s *Server) handlePic(w http.ResponseWriter, r *http.Request) {
	category := r.PathValue("category")
	file := r.PathValue("file")
	if !store.IsValidIdentifier(category) || file == "" || strings.ContainsAny(file, "/\\") {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.dataDir, category, file)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Microsecond))
	})
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/favicon.ico")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/x-icon")
	w.Write(data)
}

// handleRoot：/ 为发布主页；/分类 为 302 跳转接口。
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		s.renderHome(w, r)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ep := range s.endpoints {
		if ep.path == path {
			s.renderLink(w, r, ep)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) renderLink(w http.ResponseWriter, r *http.Request, ep endpoint) {
	category := ep.desk
	if ep.mobile != "" && mobileRe.MatchString(r.UserAgent()) {
		category = ep.mobile
	}

	// ?json=NN 按序号返回纯文本链接
	if v := r.URL.Query().Get("json"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			row, err := s.st.ByIndex(r.Context(), category, n)
			if err != nil {
				plainError(w, "No link found.", http.StatusNotFound)
				return
			}
			plainText(w, s.absoluteURL(r, row.Link))
			return
		}
	}

	// ?json 多条随机（纯文本）
	if _, ok := r.URL.Query()["json"]; ok {
		limit := positiveParam(r, "quantity", 1, s.cfg.MaxLinks)
		rows, err := s.st.Random(r.Context(), category, limit)
		if err != nil {
			plainError(w, "No links found.", http.StatusNotFound)
			return
		}
		links := make([]string, len(rows))
		for i, row := range rows {
			links[i] = s.absoluteURL(r, row.Link)
		}
		plainText(w, strings.Join(links, "\n"))
		return
	}

	rows, err := s.st.Random(r.Context(), category, 1)
	if err != nil || len(rows) == 0 {
		plainError(w, "No links found.", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, rows[0].Link, http.StatusFound)
}

// absoluteURL 把站内相对路径拼成完整 URL（供纯文本 / JSON 输出）。
// 优先使用管理员在后台配置的对外域名，其次环境变量，最后按请求推断。
func (s *Server) absoluteURL(r *http.Request, link string) string {
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return link
	}
	s.mu.RLock()
	base := s.baseURL
	s.mu.RUnlock()
	if base != "" {
		return base + link
	}
	scheme := "https"
	if r.TLS == nil {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host + link
}

func positiveParam(r *http.Request, name string, def, max int) int {
	v := r.URL.Query().Get(name)
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func plainText(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, body)
}

func plainError(w http.ResponseWriter, msg string, status int) {
	w.WriteHeader(status)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, msg)
}

// categoryCountMap 拉取各分类数量。
func (s *Server) categoryCountMap(ctx context.Context) map[string]int {
	out := map[string]int{}
	names, err := s.st.Categories(ctx)
	if err != nil {
		return out
	}
	for _, n := range names {
		if c, err := s.st.Count(ctx, n); err == nil {
			out[n] = c
		}
	}
	return out
}

// sortedCategories 返回有序分类名。
func (s *Server) sortedCategories(ctx context.Context) []string {
	names, err := s.st.Categories(ctx)
	if err != nil {
		return nil
	}
	sort.Strings(names)
	return names
}
