// Package server 组装 HTTP 路由与处理器。
package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ecyapi/internal/config"
	"ecyapi/internal/memguard"
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
	// auto 自动横竖：不做双端分类绑定，而是从 desk 这一个分类里
	// 按访问设备自动挑图——桌面端出横版、移动端出竖版。
	auto bool
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

// mobileRe 匹配移动端 UA，用于双端自适应栏目分流。
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

	// 图库隐藏分类列表，后台可编辑，持久化到 data/gallery.json
	galleryHidden []string

	// 首页文案与改动日志，后台可编辑，持久化到 data/settings.json / data/logs.json
	slogan string
	share  string
	icp    string
	icpURL string
	logs   []string

	// access 异步访问日志（/pic 大流量请求不记录）。
	access *accessLog

	// 预览压缩：按像素预算的闸门 + 按缓存键的串行锁。
	previewGate  *previewGate
	previewLocks *keyedLocks

	// 图库渲染闸门：限制同时渲染「全部图片」的请求数。
	galleryGate *galleryGate

	// previewJobs 后台补缓存队列（有界，满则丢弃）。
	previewJobs chan previewJob

	// mem 内存调节器：软目标 75%、红线 90%，按压力降档并通知上层降级。
	mem *memguard.Governor

	// prewarm 后台预热任务状态（图库页 + 可选缩略图）。
	prewarm *prewarmState

	// sso OAuth2 / OIDC 登录状态（配置、进行中的授权流程、JWKS 缓存）。
	sso *ssoState

	// staticDir /@index/* 的磁盘资源目录（看板娘动画等大文件放这里，优先于内置资源）。
	staticDir string
	// staticSyncFrom 启动时把素材从这里同步进 staticDir（Docker 用镜像层 → 数据卷）。
	staticSyncFrom string

	// groups 虚拟分组快照（/@img/<name>）。
	groups *groupSnapshot
	// homeOrder 首页卡片统一顺序（栏目与虚拟分组混排），来自 data/order.json。
	homeOrder []string
	// act 活跃任务计数（每 N 秒输出一行状态日志）。
	act activityTracker
	// hiddenVersion/hiddenCache 图库隐藏集合的版本与缓存（列表不变就复用）。
	hiddenVersion int64
	hiddenVer     int64
	hiddenCache   map[string]bool
	// staticVersion 静态资源版本号（8 位十六进制），用于 /@index/v<版本>/... 这种可长缓存的 URL。
	staticVersion string

	// login 后台登录失败限流（按来源 IP）。
	login *loginLimiter
}

// 首页文案默认值：settings.json 未配置或留空时回退到这些值。
const (
	defaultSlogan = "图片求精，往期图片随着环境的不断升级，也会不断提高分辨率"
	defaultShare  = "如果你喜欢，那就分享给喜欢二次元风格的博主们吧"
	defaultICP    = "萌ICP备20239001号"
	defaultICPURL = "https://icp.gov.moe/?keyword=20239001"
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
	// 兜底：直接构造 Config 的场景（测试、嵌入式）可能没设这些值。
	if cfg.PreviewMP <= 0 {
		cfg.PreviewMP = 32
	}
	if cfg.MemoryLimit <= 0 {
		cfg.MemoryLimit = 512 << 20
	}
	st, err := store.NewFileStoreWithOptions(cfg.DataDir, store.Options{
		MaxDepth:          cfg.MaxDepth,
		ScanInterval:      time.Duration(cfg.ScanIntervalSeconds) * time.Second,
		StartupWait:       time.Duration(cfg.StartupWaitSeconds) * time.Second,
		SnapshotTTL:       time.Duration(cfg.SnapshotTTLSeconds) * time.Second,
		SnapshotMaxAge:    time.Duration(cfg.SnapshotMaxAgeSeconds) * time.Second,
		VerifyInterval:    time.Duration(cfg.VerifyIntervalSeconds) * time.Second,
		ParentAggDirsOnly: cfg.ParentAggDirsOnly,
	})
	if err != nil {
		return nil, err
	}
	admin, _ := store.NewFileAdmin(st)
	s := &Server{
		cfg: cfg, st: st, admin: admin, dataDir: store.Dir(st),
		access:         newAccessLog(),
		previewGate:    newPreviewGate(cfg.PreviewMP),
		galleryGate:    newGalleryGate(cfg.GalleryConcurrency, time.Duration(cfg.GalleryWaitSeconds)*time.Second),
		previewJobs:    make(chan previewJob, 512),
		prewarm:        &prewarmState{},
		sso:            newSSOState(),
		staticDir:      cfg.StaticDir,
		staticSyncFrom: cfg.StaticSyncFrom,
		mem: memguard.New(memguard.Options{
			LimitBytes: cfg.MemoryLimit,
			SoftPct:    float64(cfg.MemSoftPct) / 100,
			HardPct:    float64(cfg.MemHardPct) / 100,      // 降级红线（默认 85%）
			TargetCap:  float64(cfg.MemTargetCapPct) / 100, // 自适应目标上限（默认 30%）
		}),
		previewLocks: newKeyedLocks(),
		login:        newLoginLimiter(),
	}
	if err := s.loadEndpoints(); err != nil {
		return nil, err
	}
	// 启动时把「被跳过的目录」打出来：最常见的困惑是子文件夹名字不合法
	// 或是符号链接失效，导致分类数量比磁盘上的文件夹少。
	if warns := store.ScanWarnings(st); len(warns) > 0 {
		log.Printf("扫描提示：%d 个子目录未作为分类载入（最多显示 %d 条）", len(warns), len(warns))
		for i, w := range warns {
			if i >= 10 {
				log.Printf("  … 其余 %d 条已省略（后台「分类管理」页可查看完整列表）", len(warns)-10)
				break
			}
			log.Printf("  - %s", w)
		}
	}

	s.syncStaticAssets()
	s.warnMissingPetAssets()
	s.staticVersion = s.staticAssetVersion()

	if cfg.MemoryLimit > 0 {
		base := memguard.TargetFor(cfg.MemoryLimit, 0, 0, 0)
		capMB := s.mem.TargetCap() >> 20
		log.Printf("内存策略：限额 %dMB｜目标 max(25MB, %d%%) = %dMB，按 max(索引, QPS) 放宽至上限 %dMB｜降级红线 %d%% = %dMB｜QPS 放宽阈值 %d（0=关）",
			cfg.MemoryLimit>>20, cfg.MemSoftPct, base>>20, capMB, cfg.MemHardPct,
			int64(float64(cfg.MemoryLimit)*float64(cfg.MemHardPct)/100)>>20, cfg.MemQPSThreshold)
	}
	s.baseURL = cfg.BaseURL
	s.slogan, s.share, s.icp = defaultSlogan, defaultShare, defaultICP
	s.icpURL = defaultICPURL
	if err := s.loadSettings(); err != nil {
		return nil, err
	}
	if err := s.loadLogs(); err != nil {
		return nil, err
	}
	if err := s.loadGalleryHidden(); err != nil {
		return nil, err
	}
	if err := s.loadSSO(); err != nil {
		return nil, err
	}
	if err := s.loadGroups(); err != nil {
		log.Printf("虚拟分组定义有误，已忽略：%v", err)
	}
	if err := s.loadHomeOrder(); err != nil {
		log.Printf("首页顺序读取失败，已忽略：%v", err)
	}
	funcMap := template.FuncMap{
		"Add": func(a, b int) int { return a + b },
		// HasString 判断切片里是否包含某个字符串（模板里勾选已选分类用）。
		"HasString": func(list []string, v string) bool {
			for _, x := range list {
				if x == v {
					return true
				}
			}
			return false
		},
		// RichText 允许首页文案使用白名单内的少量 HTML 标签（如 <br> 换行）。
		"RichText": renderRichText,
	}
	go s.sweepSessionsLoop()
	go s.previewWorker()
	go s.mem.Run(time.Second)
	if s.cfg.ActivityLogSeconds > 0 {
		go s.activityLoop(time.Duration(s.cfg.ActivityLogSeconds) * time.Second)
	}

	tpl, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s.tpl = tpl
	return s, nil
}

// ---------------------------------------------------------------- 双端绑定持久化

// endpointJSON endpoints.json 的单条栏目定义（读入）。
// Auto 用指针区分「文件里没写这个字段」与「显式写了 false」：
// 前者按默认规则推断（单端图片栏目默认开启自动横竖），后者严格按 false 处理。
type endpointJSON struct {
	Path   string `json:"path"`
	Desk   string `json:"desk"`
	Mobile string `json:"mobile"`
	Label  string `json:"label"`
	Desc   string `json:"desc"`
	Kind   string `json:"kind"`
	Auto   *bool  `json:"auto,omitempty"`
}

// endpointOut 写回 endpoints.json 的形态：auto 始终显式写出（包括 false），
// 否则下次加载时会因为「字段缺失」被当成默认值 true。
type endpointOut struct {
	Path   string `json:"path"`
	Desk   string `json:"desk"`
	Mobile string `json:"mobile"`
	Label  string `json:"label"`
	Desc   string `json:"desc"`
	Kind   string `json:"kind"`
	Auto   bool   `json:"auto"`
}

func (s *Server) loadEndpoints() error {
	// 不再内置任何默认栏目：所有栏目均由管理员在后台创建，从 endpoints.json 加载。
	s.endpoints = []endpoint{}
	data, err := os.ReadFile(endpointsFile)
	if err == nil {
		customs, order, perr := parseEndpointsFile(data)
		if perr != nil {
			// 配置文件损坏不应导致服务无法启动（启动失败会触发容器重启循环），
			// 记录告警后按空栏目加载，管理员可在后台重建并覆盖坏文件。
			log.Printf("警告: 解析 %s 失败（按空栏目加载）: %v", endpointsFile, perr)
		} else {
			for _, c := range customs {
				// mobile 允许为空（单端栏目），非空才校验。
				if s.validCategory(c.Path) && s.validCategory(c.Desk) &&
					(c.Mobile == "" || s.validCategory(c.Mobile)) {
					kind := c.Kind
					if kind != "video" {
						kind = "image"
					}
					// 自动横竖的默认规则：
					//   1) 视频没有方向 → 恒为 false；
					//   2) 文件里显式写了 auto → 以文件为准；
					//   3) 没写 auto 的单端图片栏目（旧版默认形态）→ 默认开启。
					//   双端绑定（mobile 非空）保持原语义不变。
					auto := false
					switch {
					case kind == "video":
						auto = false
					case c.Auto != nil:
						auto = *c.Auto
					case c.Mobile == "":
						auto = true
					}
					mobile := c.Mobile
					if auto {
						mobile = "" // 自动横竖只用一个分类
					}
					s.endpoints = append(s.endpoints, endpoint{
						path: c.Path, desk: c.Desk, mobile: mobile,
						label: c.Label, desc: c.Desc, kind: kind, auto: auto,
					})
				}
			}
			if len(order) > 0 {
				s.endpoints = applyEndpointOrder(s.endpoints, order)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// endpoints.json 不存在时栏目为空（主页无卡片、跳转接口 404），由管理员在后台创建。
	s.epIndex = endpointIndex(s.endpoints)
	return nil
}

// parseEndpointsFile 兼容两种持久化格式：
// 1) 新版对象 {"order":[...],"items":[...]}；2) 旧版纯数组 [...]。
func parseEndpointsFile(data []byte) ([]endpointJSON, []string, error) {
	// 先探测顶层 JSON 类型：数组走旧版纯数组格式，对象走新版 {"order","items"} 格式。
	var probe any
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, nil, err
	}
	if arr, ok := probe.([]any); ok {
		customs := make([]endpointJSON, 0, len(arr))
		if err := json.Unmarshal(data, &customs); err != nil {
			return nil, nil, err
		}
		return customs, nil, nil
	}
	if _, ok := probe.(map[string]any); !ok {
		return nil, nil, fmt.Errorf("unexpected top-level JSON type %T", probe)
	}
	var payload struct {
		Order []string       `json:"order"`
		Items []endpointJSON `json:"items"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil, err
	}
	if payload.Items != nil {
		return payload.Items, payload.Order, nil
	}
	// 对象但无 items：兼容历史写坏的单对象（{"path":...}），视为单条栏目。
	var single endpointJSON
	if err := json.Unmarshal(data, &single); err == nil && single.Path != "" {
		return []endpointJSON{single}, nil, nil
	}
	// 其余对象（空对象、仅 order、items 为 null 等）一律视为空栏目列表，
	// 栏目数据由管理员在后台重建，避免坏文件把服务卡死在启动循环。
	return []endpointJSON{}, nil, nil
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
// expandEndpointOrder 把「一级分类顺序」展开成完整栏目顺序：
// 同一级下的多个栏目保持它们当前的相对顺序，整体跟着一级分类走。
func (s *Server) expandEndpointOrder(tokens []string) []string {
	s.mu.RLock()
	eps := append([]endpoint(nil), s.endpoints...)
	s.mu.RUnlock()
	out := make([]string, 0, len(eps))
	used := make(map[string]bool, len(eps))
	for _, tok := range tokens {
		tok = strings.TrimPrefix(tok, "/")
		for _, ep := range eps {
			if used[ep.path] {
				continue
			}
			if topSegment(ep.path) == tok {
				out = append(out, ep.path)
				used[ep.path] = true
			}
		}
	}
	for _, ep := range eps {
		if !used[ep.path] {
			out = append(out, ep.path)
		}
	}
	return out
}

func (s *Server) reorderEndpoints(order []string) error {
	// order 里混有虚拟分组（@img/<name>）：栏目顺序与分组顺序分别落盘，
	// 另外把「混排后的整体顺序」写进 data/order.json 供首页使用。
	epOrder := make([]string, 0, len(order))
	grpOrder := make([]string, 0, len(order))
	for _, tok := range order {
		if strings.HasPrefix(tok, "@img/") {
			grpOrder = append(grpOrder, strings.TrimPrefix(tok, "@img/"))
			continue
		}
		epOrder = append(epOrder, strings.TrimPrefix(tok, "/"))
	}

	// 先在锁外把「一级分类顺序」展开成完整栏目顺序（内部要读 endpoints，不能再持写锁）
	expanded := s.expandEndpointOrder(epOrder)
	s.mu.Lock()
	next := applyEndpointOrder(s.endpoints, expanded)
	s.endpoints = next
	data, err := marshalEndpointsLocked(s.endpoints)
	s.epIndex = endpointIndex(s.endpoints)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.WriteFile(endpointsFile, data, 0o644); err != nil {
		return err
	}
	if len(grpOrder) > 0 {
		if err := s.reorderGroups(grpOrder); err != nil {
			return err
		}
	}
	canon := make([]string, 0, len(order))
	for _, tok := range order {
		canon = append(canon, canonToken(tok))
	}
	return s.saveHomeOrder(canon)
}

// removeEndpointsByCategory 删除引用了指定分类的所有栏目（PC 端或移动端命中即删），
// 并持久化到 endpoints.json。返回被下线的栏目路径列表。
func (s *Server) removeEndpointsByCategory(category string) ([]string, error) {
	s.mu.Lock()
	removed := make([]string, 0, 4)
	next := s.endpoints[:0]
	for _, ep := range s.endpoints {
		if ep.desk == category || ep.mobile == category {
			removed = append(removed, ep.path)
			continue
		}
		next = append(next, ep)
	}
	s.endpoints = next
	s.epIndex = endpointIndex(s.endpoints)
	s.mu.Unlock()
	if len(removed) == 0 {
		return removed, nil
	}
	if err := s.saveEndpoints(); err != nil {
		return removed, err
	}
	return removed, nil
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
		Order []string      `json:"order,omitempty"`
		Items []endpointOut `json:"items"`
	}{}
	for _, ep := range eps {
		payload.Order = append(payload.Order, ep.path)
		payload.Items = append(payload.Items, endpointOut{
			Path: ep.path, Desk: ep.desk, Mobile: ep.mobile,
			Label: ep.label, Desc: ep.desc, Kind: ep.kind, Auto: ep.auto,
		})
	}
	return json.MarshalIndent(payload, "", "  ")
}

// ---------------------------------------------------------------- 站点设置持久化

type settingsJSON struct {
	BaseURL string `json:"base_url"`
	Slogan  string `json:"slogan,omitempty"`
	Share   string `json:"share,omitempty"`
	ICP     string `json:"icp,omitempty"`
	ICPURL  string `json:"icp_url,omitempty"`
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
	if v := normalizeICPURL(cfg.ICPURL); v != "" {
		s.icpURL = v
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
		ICPURL:  s.icpURL,
	}
	s.mu.RUnlock()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsFile, data, 0o644)
}

// setContent 更新首页文案并持久化；slogan/share/icp 留空回退默认值，
// icpURL 留空表示不生成链接（备案号仅展示纯文本）。
func (s *Server) setContent(slogan, share, icp, icpURL string) error {
	slogan, share = strings.TrimSpace(slogan), strings.TrimSpace(share)
	icp, icpURL = strings.TrimSpace(icp), strings.TrimSpace(icpURL)
	if slogan == "" {
		slogan = defaultSlogan
	}
	if share == "" {
		share = defaultShare
	}
	if icp == "" {
		icp = defaultICP
	}
	// 备案号文字保持默认时，链接留空则回退默认官方查询链接；
	// 文字被自定义而链接留空时，视为用户不要链接。
	if icpURL == "" && icp == defaultICP {
		icpURL = defaultICPURL
	}
	icpURL = normalizeICPURL(icpURL)
	s.mu.Lock()
	s.slogan, s.share, s.icp, s.icpURL = slogan, share, icp, icpURL
	s.mu.Unlock()
	return s.saveSettings()
}

// normalizeICPURL 规整备案号链接：补协议、去首尾空白；空串或不含主机时返回空串（不生成链接）。
func normalizeICPURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		v = "https://" + v
	}
	if strings.ContainsAny(v, " \t\r\n\"<>") {
		return ""
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://")
	if rest == "" || strings.HasPrefix(rest, "/") {
		return ""
	}
	return v
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

// galleryHiddenFile 图库隐藏分类持久化文件，存于数据目录外。
const galleryHiddenFile = "data/gallery.json"

// loadGalleryHidden 读取图库隐藏分类列表；文件不存在时全部可见。
func (s *Server) loadGalleryHidden() error {
	data, err := os.ReadFile(galleryHiddenFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var hidden []string
	if err := json.Unmarshal(data, &hidden); err != nil {
		return fmt.Errorf("parse %s: %w", galleryHiddenFile, err)
	}
	s.galleryHidden = hidden
	s.hiddenVersion++
	return nil
}

func (s *Server) saveGalleryHidden() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.galleryHidden, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(galleryHiddenFile, data, 0o644)
}

// toggleGalleryHidden 切换分类在图库页的显示状态并持久化。
func (s *Server) toggleGalleryHidden(category string) (bool, error) {
	s.mu.Lock()
	hidden := false
	next := make([]string, 0, len(s.galleryHidden))
	for _, c := range s.galleryHidden {
		if c == category {
			hidden = true // 原本隐藏，本次取消隐藏
			continue
		}
		next = append(next, c)
	}
	if !hidden {
		next = append(next, category)
	}
	s.galleryHidden = next
	s.hiddenVersion++
	s.mu.Unlock()
	return !hidden, s.saveGalleryHidden()
}

// galleryHiddenSet 返回隐藏分类集合快照。
// galleryHiddenSet 图库隐藏集合。按「隐藏列表版本号」缓存：
// 列表不变时直接复用同一份 map，避免每个 /@img 请求都重建。
func (s *Server) galleryHiddenSet() map[string]bool {
	s.mu.RLock()
	ver := s.hiddenVersion
	if s.hiddenCache != nil && s.hiddenVer == ver {
		set := s.hiddenCache
		s.mu.RUnlock()
		return set
	}
	hidden := append([]string(nil), s.galleryHidden...)
	s.mu.RUnlock()

	set := make(map[string]bool, len(hidden))
	for _, c := range hidden {
		set[c] = true
	}
	s.mu.Lock()
	if s.hiddenVersion == ver {
		s.hiddenCache, s.hiddenVer = set, ver
	}
	s.mu.Unlock()
	return set
}

// pruneGalleryHidden 清理已不存在的隐藏分类记录。
func (s *Server) pruneGalleryHidden(categories []string) error {
	exist := make(map[string]bool, len(categories))
	for _, c := range categories {
		exist[c] = true
	}
	s.mu.Lock()
	next := s.galleryHidden[:0]
	for _, c := range s.galleryHidden {
		if exist[c] {
			next = append(next, c)
		}
	}
	s.galleryHidden = next
	s.hiddenVersion++
	s.mu.Unlock()
	return s.saveGalleryHidden()
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

// addBinding 新增双端自适应绑定栏目（必须同时绑定 PC 与移动端分类）。
func (s *Server) addBinding(path, desk, mobile, label, desc string) error {
	if !s.validCategory(mobile) {
		return fmt.Errorf("移动端分类路径不合法（首段字母开头，每段仅字母/数字/下划线，最多 3 段）")
	}
	return s.addEndpoint(path, desk, mobile, label, desc)
}

// addAdaptive 新增「自动横竖」栏目：只绑定一个分类，同一个分类里
// 桌面端取横图、移动端取竖图，不再需要人工准备两个分类。
func (s *Server) addAdaptive(path, desk, label, desc string) error {
	return s.addEndpointSpec(endpoint{
		path: path, desk: desk, label: label, desc: desc, kind: "image", auto: true,
	})
}

// addEndpoint 新增栏目：path 为访问路径，desk/mobile 为数据源分类（mobile 为空表示单端）。
func (s *Server) addEndpoint(path, desk, mobile, label, desc string) error {
	return s.addEndpointSpec(endpoint{
		path: path, desk: desk, mobile: mobile, label: label, desc: desc, kind: "image",
	})
}

// addEndpointSpec 校验并持久化一个栏目。
func (s *Server) addEndpointSpec(ep endpoint) error {
	if !s.validCategory(ep.path) {
		return fmt.Errorf("栏目路径不合法（不能以 . 开头、不能含 ? # %% 或控制字符；最多 %d 段、每段 ≤128 字节）", s.cfg.MaxDepth)
	}
	if hasReservedTop(ep.path) {
		return fmt.Errorf("栏目路径不能以 %s 开头（与内置接口冲突）", strings.SplitN(ep.path, "/", 2)[0])
	}
	if !s.validCategory(ep.desk) {
		return fmt.Errorf("分类路径不合法")
	}
	if ep.kind == "video" {
		ep.auto = false // 视频没有横竖之分
	}
	if ep.auto {
		ep.mobile = "" // 自动横竖只用一个分类，忽略移动端绑定
	} else if ep.mobile != "" && !s.validCategory(ep.mobile) {
		return fmt.Errorf("移动端分类路径不合法")
	}
	if ep.label == "" {
		ep.label = ep.path
	}
	if ep.kind == "" {
		ep.kind = "image"
	}
	s.mu.Lock()
	for _, e := range s.endpoints {
		if e.path == ep.path {
			s.mu.Unlock()
			return fmt.Errorf("栏目 %s 已存在", ep.path)
		}
	}
	s.endpoints = append(s.endpoints, ep)
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
	mux.HandleFunc("GET /pic/", s.handlePic)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
	mux.HandleFunc("GET /admin", s.handleAdmin)
	mux.HandleFunc("POST /admin", s.handleAdmin)
	mux.HandleFunc("POST /admin/update", s.handleAdminUpdate)
	mux.HandleFunc("POST /admin/create", s.handleAdminCreate)
	mux.HandleFunc("POST /admin/upload", s.handleAdminUpload)
	mux.HandleFunc("POST /admin/delete", s.handleAdminDelete)
	mux.HandleFunc("POST /admin/category/delete", s.handleAdminCategoryDelete)
	mux.HandleFunc("POST /admin/gallery/visibility", s.handleAdminGalleryVisibility)
	mux.HandleFunc("POST /admin/binding", s.handleAdminBinding)
	mux.HandleFunc("POST /admin/settings", s.handleAdminSettings)
	mux.HandleFunc("POST /admin/content", s.handleAdminContent)
	mux.HandleFunc("POST /admin/logs/add", s.handleAdminLogAdd)
	mux.HandleFunc("POST /admin/logs/delete", s.handleAdminLogDelete)
	mux.HandleFunc("POST /admin/endpoints/reorder", s.handleAdminEndpointsReorder)
	mux.HandleFunc("GET /admin/data", s.handleAdminData)
	mux.HandleFunc("POST /admin/cache/clear", s.handleAdminCacheClear)
	mux.HandleFunc("POST /admin/cache/prewarm", s.handleAdminCachePrewarm)
	mux.HandleFunc("GET /admin/cache/status", s.handleAdminCacheStatus)
	mux.HandleFunc("POST /admin/cache/cancel", s.handleAdminCacheCancel)
	mux.HandleFunc("POST /admin/groups/upsert", s.handleAdminGroupUpsert)
	mux.HandleFunc("POST /admin/groups/delete", s.handleAdminGroupDelete)

	// 管理登录的 OAuth2 / OIDC
	mux.HandleFunc("GET /admin/sso/login", s.handleSSOLogin)
	mux.HandleFunc("GET /admin/sso/callback", s.handleSSOCallback)
	mux.HandleFunc("POST /admin/sso/config", s.handleSSOConfig)
	mux.HandleFunc("POST /admin/sso/discover", s.handleSSODiscover)
	mux.HandleFunc("POST /admin/sso/unbind", s.handleSSOUnbind)
	// 内置静态资源（看板娘）：磁盘目录优先，embed 兜底。
	mux.HandleFunc("GET /@index/", s.handleIndexStatic)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		// 扫描中仍然返回 200（进程健康、能服务），但把状态标出来，便于排查
		// 「刚启动时分类不全」；需要严格就绪探针的编排可以据此判断。
		if s.st.Scanning() {
			fmt.Fprint(w, "ok scanning")
			return
		}
		fmt.Fprint(w, "ok")
	})
	// /readyz 是严格就绪：首次扫描没跑完就返回 503，供 K8s readinessProbe 使用。
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.st.Scanning() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "scanning")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ready")
	})

	return s.logMiddleware(corsMiddleware(mux))
}

// splitPicPath 把 /pic/a/b/f.png 拆成分类 "a/b" 与文件名 "f.png"。
// 最后一段固定是文件名，其余段是分类路径；段数不足或为空时返回 false。
func splitPicPath(p string) (category, file string, ok bool) {
	rel := strings.TrimPrefix(p, "/pic/")
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return "", "", false
	}
	i := strings.LastIndexByte(rel, '/')
	if i <= 0 || i == len(rel)-1 {
		return "", "", false
	}
	return rel[:i], rel[i+1:], true
}

// validCategory 用当前配置的层级上限（ECY_MAX_DEPTH）校验分类/栏目路径。
func (s *Server) validCategory(name string) bool {
	return store.IsValidCategoryDepth(name, s.cfg.MaxDepth)
}

// hasReservedTop 判断分类路径/栏目路径的首段是否与内置路由冲突。
// 规则与 store 扫描共用一份（store.IsReservedTop），避免两边不一致。
func hasReservedTop(name string) bool { return store.IsReservedTop(name) }

// handlePic 提供本地图片访问：/pic/{分类路径}/{文件名}。
// 分类路径支持多级子目录，如 /pic/a/b/f.png 对应分类 a/b 下的 f.png。
func (s *Server) handlePic(w http.ResponseWriter, r *http.Request) {
	category, file, ok := splitPicPath(r.URL.Path)
	if !ok || !s.validCategory(category) || strings.ContainsAny(file, "/\\") {
		http.NotFound(w, r)
		return
	}
	// ?w=NNN 压缩预览：仅供站内页面缩略展示，不改变原图输出。
	// 判断前先移除 w 参数，预览失败回退时直接走下方原图逻辑，避免递归。
	if widthParam := r.URL.Query().Get("w"); widthParam != "" {
		q := r.URL.Query()
		q.Del("w")
		r.URL.RawQuery = q.Encode()
		s.handlePreview(w, r, category, file, widthParam)
		return
	}
	// 扩展名白名单：分类目录里的非图片文件不对外输出。
	ct, ok := contentTypeFor(file)
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.dataDir, filepath.FromSlash(category), file)
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
	cacheLong(w.Header())
	serveLocalFile(w, r, f, fi, ct)
}

// logMiddleware 记录访问日志。图片等大流量静态请求直接跳过；
// 其余请求投递给异步 writer，热路径上不加锁、不格式化。
func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 记录活跃时间（供内存调节器与预热让路判断）。
		// 后台自己的请求（/admin）不算活跃：管理员开着状态页轮询，
		// 不应把预热任务饿死。
		if !strings.HasPrefix(r.URL.Path, "/admin") {
			s.mem.NoteRequest()
		}
		// 统计在途请求与完成速率（活跃任务日志用）
		defer s.noteRequestStart()()
		if strings.HasPrefix(r.URL.Path, "/pic/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		s.access.add(r.Method, r.URL.Path, time.Since(start))
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
	// 全分类随机：/@img 从所有已注册分类里抽一个，横竖按设备自动。
	if strings.EqualFold(path, "@img") {
		s.serveAllRandom(w, r)
		return
	}
	// 虚拟分组：/@img/<name>（单级）
	if strings.HasPrefix(strings.ToLower(path), "@img/") {
		name := path[len("@img/"):]
		if name == "" || strings.Contains(name, "/") {
			plainError(w, "No such group.", http.StatusNotFound)
			return
		}
		s.handleGroup(w, r, strings.ToLower(name))
		return
	}
	s.mu.RLock()
	ep, ok := s.epIndex[path]
	s.mu.RUnlock()
	if ok {
		s.renderLink(w, r, ep)
		return
	}
	// 磁盘目录即接口：没有在后台建过栏目的分类，URL 也能直接用。
	// 方向按设备自动判定（桌面横图 / 移动竖图），可用 ?orientation= 覆盖。
	if s.validCategory(path) {
		if _, err := s.st.CountWith(r.Context(), path, store.OrientationAny); err == nil {
			s.renderLink(w, r, endpoint{
				path: path, desk: path, label: path, kind: "image", auto: true,
			})
			return
		}
	}
	http.NotFound(w, r)
}

// redirectRandom 输出 302 到随机结果。
//
// 必须禁止缓存：同一个入口每次都应给不同图片。如果 CDN/浏览器缓存了这条 302，
// 该入口的随机性就被"冻住"——所有用户长期拿到同一张。随机性本身由服务端抽样决定，
// 与「是否提前拉平」无关，但缓存会把它变成确定值。
func redirectRandom(w http.ResponseWriter, r *http.Request, link string) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	http.Redirect(w, r, link, http.StatusFound)
}

// serveAllRandom 全分类随机：先从「所有已注册分类」里随机挑一个，再在该分类里随机取图。
// 横竖按设备自动（桌面横图 / 移动竖图），可用 ?orientation= 覆盖；
// 抽到的分类恰好没有该方向的图（或全是死链）时会换一个分类再试，
// 避免出现「某些分类永远抽不到图」的空转。
func (s *Server) serveAllRandom(w http.ResponseWriter, r *http.Request) {
	want := linkOrientation(r, endpoint{auto: true})
	all, err := s.st.Categories(r.Context())
	if err != nil || len(all) == 0 {
		plainError(w, "No links found.", http.StatusNotFound)
		return
	}
	// 图库隐藏的分类同样不参与全分类随机（与 /img 的可见性保持一致）。
	hidden := s.galleryHiddenSet()
	cats := make([]string, 0, len(all))
	for _, c := range all {
		if !hidden[c] {
			cats = append(cats, c)
		}
	}
	if len(cats) == 0 {
		plainError(w, "No links found.", http.StatusNotFound)
		return
	}

	suffix := ""
	if v := r.URL.Query().Get("w"); v != "" {
		suffix = "?w=" + url.QueryEscape(v)
	}

	start := rand.IntN(len(cats))
	var picked store.Row
	found := false
	// 最多试 8 个（或全部分类），够避开空分类，又不会在百万分类时扫太久。
	tries := len(cats)
	if tries > 8 {
		tries = 8
	}
	for i := 0; i < tries; i++ {
		cat := cats[(start+i)%len(cats)]
		if row, ok := s.pickVerified(r, cat, want); ok {
			picked, found = row, true
			break
		}
	}
	if !found {
		plainError(w, "No links found.", http.StatusNotFound)
		return
	}

	// ?json 返回纯文本链接，与 /{分类}?json 的行为保持一致。
	if _, ok := r.URL.Query()["json"]; ok {
		plainText(w, s.absoluteURL(r, picked.Link)+suffix)
		return
	}
	redirectRandom(w, r, picked.Link+suffix)
}

// resolveOrientation 解析方向参数；auto 表示跟随 UA（移动端竖图、桌面横图）。
func resolveOrientation(v string, r *http.Request) (store.Orientation, bool) {
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, "auto") {
		if mobileRe.MatchString(r.UserAgent()) {
			return store.OrientationPortrait, true
		}
		return store.OrientationLandscape, true
	}
	return store.ParseOrientation(v)
}

// linkOrientation 决定本次请求取横图还是竖图。
// 优先级：?orientation= 显式指定 > 栏目「自动横竖」（按 UA）> 不限方向。
func linkOrientation(r *http.Request, ep endpoint) store.Orientation {
	if v := r.URL.Query().Get("orientation"); v != "" {
		if o, ok := resolveOrientation(v, r); ok {
			return o
		}
	}
	if ep.auto {
		if mobileRe.MatchString(r.UserAgent()) {
			return store.OrientationPortrait
		}
		return store.OrientationLandscape
	}
	return store.OrientationAny
}

// verifyRow 校验一条记录指向的文件是否仍在磁盘上。
// 注意用 Link 反解出真实分类路径：父目录聚合出的记录，文件在子分类下，
// 直接拿聚合路径去 stat 会误判为死链。
func (s *Server) verifyRow(r *http.Request, row store.Row) bool {
	cat, file, ok := splitPicPath(row.Link)
	if !ok {
		return false
	}
	return s.st.Verify(r.Context(), cat, file)
}

// pickVerified 随机取一条并确认文件仍在磁盘上。
// 后台刷新有最长一个扫描间隔的滞后，刚被删掉的文件可能仍在索引里；
// 这里发现即从索引摘除并重取，最多试 3 次，避免把用户 302 到 404。
func (s *Server) pickVerified(r *http.Request, category string, want store.Orientation) (store.Row, bool) {
	if s.cfg.DisableLinkVerify {
		rows, err := s.randomRows(r, category, 1, want)
		if err != nil || len(rows) == 0 {
			return store.Row{}, false
		}
		return rows[0], true
	}
	for attempt := 0; attempt < 3; attempt++ {
		rows, err := s.randomRows(r, category, 1, want)
		if err != nil || len(rows) == 0 {
			return store.Row{}, false
		}
		if s.verifyRow(r, rows[0]) {
			return rows[0], true
		}
	}
	return store.Row{}, false
}

// randomRows 按方向取图；该方向没有图时回退全集，保证接口不会因为
// 「分类里暂时还没有竖图」而 404。
func (s *Server) randomRows(r *http.Request, category string, limit int, want store.Orientation) ([]store.Row, error) {
	rows, err := s.st.RandomWith(r.Context(), category, limit, want)
	if (err != nil || len(rows) == 0) && want != store.OrientationAny {
		return s.st.RandomWith(r.Context(), category, limit, store.OrientationAny)
	}
	return rows, err
}

func (s *Server) renderLink(w http.ResponseWriter, r *http.Request, ep endpoint) {
	category := ep.desk
	if ep.mobile != "" && mobileRe.MatchString(r.UserAgent()) {
		category = ep.mobile
	}
	want := linkOrientation(r, ep)
	// ?w=NNN 压缩宽度：透传到输出链接（302 Location / 纯文本链接），
	// 宽度校验由 /pic 预览侧完成，非法值等于不压缩。
	suffix := ""
	if v := r.URL.Query().Get("w"); v != "" {
		suffix = "?w=" + url.QueryEscape(v)
	}

	// ?json=NN 按序号返回纯文本链接
	if v := r.URL.Query().Get("json"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			row, err := s.st.ByIndexWith(r.Context(), category, n, want)
			if err != nil && want != store.OrientationAny {
				row, err = s.st.ByIndexWith(r.Context(), category, n, store.OrientationAny)
			}
			if err == nil && !s.cfg.DisableLinkVerify && !s.verifyRow(r, row) {
				// 该序号对应的文件已删除：索引已变化，退回一条随机可用链接。
				row, err = store.Row{}, store.ErrNotFound
				if alt, ok := s.pickVerified(r, category, want); ok {
					row, err = alt, nil
				}
			}
			if err != nil {
				plainError(w, "No link found.", http.StatusNotFound)
				return
			}
			plainText(w, s.absoluteURL(r, row.Link)+suffix)
			return
		}
	}

	// ?json 多条随机（纯文本）
	if _, ok := r.URL.Query()["json"]; ok {
		limit := positiveParam(r, "quantity", 1, s.cfg.MaxLinks)
		rows, err := s.randomRows(r, category, limit, want)
		if err != nil {
			plainError(w, "No links found.", http.StatusNotFound)
			return
		}
		links := make([]string, len(rows))
		for i, row := range rows {
			links[i] = s.absoluteURL(r, row.Link) + suffix
		}
		plainText(w, strings.Join(links, "\n"))
		return
	}

	row, ok := s.pickVerified(r, category, want)
	if !ok {
		plainError(w, "No links found.", http.StatusNotFound)
		return
	}
	redirectRandom(w, r, row.Link+suffix)
}

// baseFor 返回本次请求的对外基础地址（不含尾部斜杠）。
// 优先使用管理员配置的域名（含环境变量），其次按请求推断。
// 同一请求内多个链接请复用它 + joinURL，避免每行都取一次锁。
func (s *Server) baseFor(r *http.Request) string {
	s.mu.RLock()
	base := s.baseURL
	s.mu.RUnlock()
	if base != "" {
		return base
	}
	scheme := "https"
	if r.TLS == nil {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}

// joinURL 拼接对外链接；已是绝对地址时只补后缀。
func joinURL(base, link, suffix string) string {
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return link + suffix
	}
	return base + link + suffix
}

// absoluteURL 单次拼接（保留给纯文本等零散调用）。
func (s *Server) absoluteURL(r *http.Request, link string) string {
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return link
	}
	return s.baseFor(r) + link
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
	// Header 必须在 WriteHeader 之前设置：顺序颠倒时状态行已发出，
	// 后设的 Content-Type 被丢弃，浏览器面对无类型的 404 会按下载处理。
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprint(w, msg)
}

// categoryCountMap 拉取各分类数量。
// categoryCountMap 分类 → 图片数。直接用 store 的代次缓存，不再逐个 Count（每次都要拿锁）。
func (s *Server) categoryCountMap(ctx context.Context) map[string]int {
	return s.st.Counts(ctx)
}

// sortedCategories 返回有序分类名。
// sortedCategories 分类名（store 已排序并缓存，这里不再重复排序或复制）。
func (s *Server) sortedCategories(ctx context.Context) []string {
	names, err := s.st.Categories(ctx)
	if err != nil {
		return nil
	}
	return names
}
