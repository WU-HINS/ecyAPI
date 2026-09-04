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

// endpointIndex 以路径为键的端点索引，O(1) 查找。
func endpointIndex(eps []endpoint) map[string]endpoint {
	m := make(map[string]endpoint, len(eps))
	for _, ep := range eps {
		m[ep.path] = ep
	}
	return m
}

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
	epIndex   map[string]endpoint // path -> endpoint，与 endpoints 同步维护
	dataDir   string
	baseURL   string // 对外展示的 API 域名（含协议），留空按请求推断

	// 首页文案与改动日志，后台可编辑，持久化到 data/settings.json / data/logs.json
	slogan string
	share  string
	icp    string
	logs   []string
}

// 首页文案默认值：settings.json 未配置或留空时回退到这些值。
const (
	defaultSlogan = "图片求精，往期图片随着环境的不断升级，也会不断提高分辨率"
	defaultShare  = "如果你喜欢，那就分享给喜欢二次元风格的博主们吧"
	defaultICP    = "萌ICP备20239001号"
)

// logsFile 改动日志持久化文件，存于数据目录外。
const logsFile = "data/logs.json"

// defaultLogs 改动日志默认内容（logs.json 不存在时使用）。
var defaultLogs = []string{
	"2026.9 — 使用 Go 语言重写全站，单二进制部署，默认文件数据源开箱即用，兼容 MySQL。",
	"2026.4.30 — 重新构建了规范的 JSON 参数，新增 pixiv 分类。",
	"2025.6.27 — 部署腾讯免费 CDN，同时增加 112 张图片。",
	"2024.5.24 — 更换了域名和服务器，添加了 200 余张图片进入各分类图池。",
	"2024.4.24 — 完善了程序后端依赖了数据库，前端更快的输出图片。",
	"2024.4.21 — 图片数据库切换到 PGSQL，增强速度与稳定性。",
	"2024.1.26 — 向冷门分类添加了大量图片，开放了捐助页面。",
	"2023.11.30 — 向各分类添加本月积累图片，添加三百余张原神图片。",
	"2023.11.1 — 输出图片格式全部改为 webp，上线 json 功能。",
	"2023.06.17 — 逐步改为原图体积输出，增加 AI 画图专栏。",
	"2023.05.11 — 对所有 api 优化取消了跳转，同时增加了小狐狸的图包。",
	"2023.05.6 — 增加试验性项目自适应设备。",
	"2023.04.21 — 更换 API 主页样式。",
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
	s.slogan, s.share, s.icp = defaultSlogan, defaultShare, defaultICP
	if err := s.loadSettings(); err != nil {
		return nil, err
	}
	if err := s.loadLogs(); err != nil {
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
	if err == nil {
		customs, order, perr := parseEndpointsFile(data)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", endpointsFile, perr)
		}
		for _, c := range customs {
			// mobile 允许为空（单端栏目），非空才校验。
			if store.IsValidIdentifier(c.Path) && store.IsValidIdentifier(c.Desk) &&
				(c.Mobile == "" || store.IsValidIdentifier(c.Mobile)) {
				s.endpoints = append(s.endpoints, endpoint{c.Path, c.Desk, c.Mobile, c.Label, c.Desc, "image"})
			}
		}
		if len(order) > 0 {
			s.endpoints = applyEndpointOrder(s.endpoints, order)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// endpoints.json 不存在时走默认列表，索引同样需要构建，否则分类跳转全部 404。
	s.epIndex = endpointIndex(s.endpoints)
	return nil
}

// parseEndpointsFile 兼容两种持久化格式：
// 1) 新版对象 {"order":[...],"items":[...]}；2) 旧版纯数组 [...]。
func parseEndpointsFile(data []byte) ([]endpointJSON, []string, error) {
	var payload struct {
		Order []string       `json:"order"`
		Items []endpointJSON `json:"items"`
	}
	if err := json.Unmarshal(data, &payload); err == nil && payload.Items != nil {
		return payload.Items, payload.Order, nil
	}
	var customs []endpointJSON
	if err := json.Unmarshal(data, &customs); err != nil {
		return nil, nil, err
	}
	return customs, nil, nil
}

// applyEndpointOrder 按 order 重排：order 中已知路径按序排前，其余保持原相对顺序排后。
func applyEndpointOrder(eps []endpoint, order []string) []endpoint {
	byPath := make(map[string]endpoint, len(eps))
	for _, ep := range eps {
		byPath[ep.path] = ep
	}
	used := make(map[string]bool, len(eps))
	out := make([]endpoint, 0, len(eps))
	for _, p := range order {
		if ep, ok := byPath[p]; ok && !used[p] {
			out = append(out, ep)
			used[p] = true
		}
	}
	for _, ep := range eps {
		if !used[ep.path] {
			out = append(out, ep)
		}
	}
	return out
}

// reorderEndpoints 按给定路径序列整体重排栏目并持久化。
// 未包含在 order 中的已知栏目保持原相对顺序排在后面，防丢。
func (s *Server) reorderEndpoints(order []string) error {
	s.mu.Lock()
	next := applyEndpointOrder(s.endpoints, order)
	s.endpoints = next
	data, err := marshalEndpointsLocked(s.endpoints)
	s.epIndex = endpointIndex(s.endpoints)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(endpointsFile, data, 0o644)
}

func (s *Server) saveEndpoints() error {
	s.mu.RLock()
	data, err := marshalEndpointsLocked(s.endpoints)
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(endpointsFile, data, 0o644)
}

// marshalEndpointsLocked 序列化 endpoints 为持久化 JSON，首元素附带当前展示顺序。
// 调用方必须已持有 s.mu（读锁或写锁）。
func marshalEndpointsLocked(eps []endpoint) ([]byte, error) {
	payload := struct {
		Order []string       `json:"order,omitempty"`
		Items []endpointJSON `json:"items"`
	}{}
	for _, ep := range eps {
		payload.Order = append(payload.Order, ep.path)
		if !isDefaultEndpoint(ep.path) {
			payload.Items = append(payload.Items, endpointJSON{ep.path, ep.desk, ep.mobile, ep.label, ep.desc, ep.kind})
		}
	}
	return json.MarshalIndent(payload, "", "  ")
}

// ---------------------------------------------------------------- 站点设置持久化

type settingsJSON struct {
	BaseURL string `json:"base_url"`
	Slogan  string `json:"slogan,omitempty"`
	Share   string `json:"share,omitempty"`
	ICP     string `json:"icp,omitempty"`
}

// loadSettings 读取站点设置；文件中已保存的域名优先于环境变量，文案覆盖默认值。
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
	if v := strings.TrimSpace(cfg.Slogan); v != "" {
		s.slogan = v
	}
	if v := strings.TrimSpace(cfg.Share); v != "" {
		s.share = v
	}
	if v := strings.TrimSpace(cfg.ICP); v != "" {
		s.icp = v
	}
	return nil
}

func (s *Server) saveSettings() error {
	s.mu.RLock()
	cfg := settingsJSON{
		BaseURL: s.baseURL,
		Slogan:  s.slogan,
		Share:   s.share,
		ICP:     s.icp,
	}
	s.mu.RUnlock()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsFile, data, 0o644)
}

// setContent 更新首页文案并持久化；留空的字段回退到默认值。
func (s *Server) setContent(slogan, share, icp string) error {
	slogan, share, icp = strings.TrimSpace(slogan), strings.TrimSpace(share), strings.TrimSpace(icp)
	if slogan == "" {
		slogan = defaultSlogan
	}
	if share == "" {
		share = defaultShare
	}
	if icp == "" {
		icp = defaultICP
	}
	s.mu.Lock()
	s.slogan, s.share, s.icp = slogan, share, icp
	s.mu.Unlock()
	return s.saveSettings()
}

// ---------------------------------------------------------------- 改动日志持久化

// loadLogs 读取改动日志；文件不存在时使用内置默认列表。
func (s *Server) loadLogs() error {
	data, err := os.ReadFile(logsFile)
	if err != nil {
		if os.IsNotExist(err) {
			s.logs = append([]string(nil), defaultLogs...)
			return nil
		}
		return err
	}
	var logs []string
	if err := json.Unmarshal(data, &logs); err != nil {
		return fmt.Errorf("parse %s: %w", logsFile, err)
	}
	s.logs = logs
	return nil
}

func (s *Server) saveLogs() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.logs, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(logsFile, data, 0o644)
}

// addLog 在日志顶部新增一条。
func (s *Server) addLog(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("日志内容不能为空")
	}
	if len([]rune(text)) > 500 {
		return fmt.Errorf("日志内容过长（最多 500 字）")
	}
	s.mu.Lock()
	s.logs = append([]string{text}, s.logs...)
	s.mu.Unlock()
	return s.saveLogs()
}

// deleteLog 按下标删除一条日志。
func (s *Server) deleteLog(index int) error {
	s.mu.Lock()
	if index < 0 || index >= len(s.logs) {
		s.mu.Unlock()
		return fmt.Errorf("日志序号不存在")
	}
	s.logs = append(s.logs[:index], s.logs[index+1:]...)
	s.mu.Unlock()
	return s.saveLogs()
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

// addBinding 新增双端自适应绑定栏目（必须同时绑定 PC 与移动端分类）。
func (s *Server) addBinding(path, desk, mobile, label, desc string) error {
	if !store.IsValidIdentifier(mobile) {
		return fmt.Errorf("移动端分类名不合法")
	}
	return s.addEndpoint(path, desk, mobile, label, desc)
}

// addEndpoint 新增栏目：path 为访问路径，desk/mobile 为数据源分类（mobile 为空表示单端）。
func (s *Server) addEndpoint(path, desk, mobile, label, desc string) error {
	if !store.IsValidIdentifier(path) {
		return fmt.Errorf("栏目路径不合法")
	}
	if !store.IsValidIdentifier(desk) {
		return fmt.Errorf("分类名不合法")
	}
	if mobile != "" && !store.IsValidIdentifier(mobile) {
		return fmt.Errorf("移动端分类名不合法")
	}
	if label == "" {
		label = path
	}
	s.mu.Lock()
	for _, ep := range s.endpoints {
		if ep.path == path {
			s.mu.Unlock()
			return fmt.Errorf("栏目 %s 已存在", path)
		}
	}
	s.endpoints = append(s.endpoints, endpoint{path, desk, mobile, label, desc, "image"})
	s.epIndex = endpointIndex(s.endpoints)
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
	mux.HandleFunc("POST /admin/content", s.handleAdminContent)
	mux.HandleFunc("POST /admin/logs/add", s.handleAdminLogAdd)
	mux.HandleFunc("POST /admin/logs/delete", s.handleAdminLogDelete)
	mux.HandleFunc("POST /admin/endpoints/reorder", s.handleAdminEndpointsReorder)
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
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	// 图片内容稳定：公开缓存 7 天，配合 CDN 可把源站压力降到接近零。
	w.Header().Set("Cache-Control", "public, max-age=604800, stale-while-revalidate=86400")
	http.ServeContent(w, r, file, fi.ModTime(), f)
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 图片等大流量静态请求跳过日志，避免标准库 log 互斥锁成为热点。
		if strings.HasPrefix(r.URL.Path, "/pic/") {
			next.ServeHTTP(w, r)
			return
		}
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
	ep, ok := s.epIndex[path]
	s.mu.RUnlock()
	if ok {
		s.renderLink(w, r, ep)
		return
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
