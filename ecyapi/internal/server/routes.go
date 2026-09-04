// Package server 组装 HTTP 路由与处理器。
package server

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
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

var endpoints = []endpoint{
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
	cfg      config.Config
	st       store.Store
	admin    store.Admin
	tpl      *template.Template
	sessions sync.Map // token -> expiry
}

// New 创建服务。dbDSN 非空时使用 MySQL，否则文件数据源。
func New(cfg config.Config, dbDSN string) (*Server, error) {
	var (
		st    store.Store
		admin store.Admin
		err   error
	)
	if dbDSN != "" {
		st, err = store.NewMySQLStore(dbDSN)
		if err != nil {
			log.Printf("mysql 不可用，降级为文件数据源: %v", err)
		} else {
			admin, _ = store.NewMySQLAdmin(st)
		}
	}
	if st == nil {
		st, err = store.NewFileStore(cfg.DataDir)
		if err != nil {
			return nil, err
		}
		admin, _ = store.NewFileAdmin(st)
	}
	funcMap := template.FuncMap{
		"Add": func(a, b int) int { return a + b },
	}
	tpl, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, st: st, admin: admin, tpl: tpl}, nil
}

// Handler 返回路由根处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("GET /docs", s.handleDocs)
	mux.HandleFunc("GET /img", s.handleGallery)
	mux.HandleFunc("GET /json", s.handleJSON)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
	mux.HandleFunc("GET /admin", s.handleAdmin)
	mux.HandleFunc("POST /admin", s.handleAdmin)
	mux.HandleFunc("POST /admin/update", s.handleAdminUpdate)
	mux.HandleFunc("POST /admin/create", s.handleAdminCreate)
	mux.HandleFunc("GET /admin/data", s.handleAdminData)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	return logMiddleware(mux)
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
	for _, ep := range endpoints {
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
			plainText(w, row.Link)
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
			links[i] = row.Link
		}
		plainText(w, strings.Join(links, "\n"))
		return
	}

	rows, err := s.st.Random(r.Context(), category, 1)
	if err != nil || len(rows) == 0 {
		plainError(w, "No links found.", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, cleanHeader(rows[0].Link), http.StatusFound)
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

func cleanHeader(v string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(strings.TrimSpace(v))
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
