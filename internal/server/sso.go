package server

// sso.go — 管理登录的 OAuth2 / OIDC 支持。
//
// 设计要点：
//   - 发现端点可选：填 issuer 或 .well-known 地址，点「发现」自动填充授权/token/jwks/userinfo；
//   - 自动判定模式：有 id_token 就按 OIDC 校验 JWT（RS/PS/ES 全系）；没有 id_token 就退回
//     OAuth2，用 userinfo 取身份；
//   - 未绑定的身份一律拒绝：管理员必须在后台点「绑定当前账号」把它写进白名单，
//     之后才能用该身份登录；
//   - 「仅使用 SSO」开启后：/admin 未登录直接跳转 SSO，密码登录被拒绝。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"ecyapi/internal/oidc"
)

const ssoFile = "data/sso.json"

// ssoBinding 一条已绑定身份。
type ssoBinding struct {
	Issuer  string `json:"issuer"`
	Sub     string `json:"sub"`
	Label   string `json:"label"`
	BoundAt string `json:"bound_at"`
}

// ssoProvider SSO 配置（持久化到 data/sso.json）。
type ssoProvider struct {
	Enabled               bool         `json:"enabled"`
	SSOOnly               bool         `json:"sso_only"`
	Issuer                string       `json:"issuer"`
	AuthorizationEndpoint string       `json:"authorization_endpoint"`
	TokenEndpoint         string       `json:"token_endpoint"`
	UserinfoEndpoint      string       `json:"userinfo_endpoint"`
	JWKSUri               string       `json:"jwks_uri"`
	ClientID              string       `json:"client_id"`
	ClientSecret          string       `json:"client_secret"`
	Scopes                string       `json:"scopes"`
	RedirectURL           string       `json:"redirect_url"`
	Mode                  string       `json:"mode"` // auto | oidc | oauth
	Bindings              []ssoBinding `json:"bindings"`
	// DetectedMode 最近一次登录自动判定出的模式（只用于展示）。
	DetectedMode string `json:"detected_mode,omitempty"`
}

// ssoFlow 进行中的授权码流程（state -> 流程数据，10 分钟过期）。
type ssoFlow struct {
	verifier string
	nonce    string
	bind     bool
	expire   time.Time
}

// ssoState 运行期状态。
type ssoState struct {
	mu      sync.Mutex
	flows   map[string]*ssoFlow
	cfg     ssoProvider
	hc      *http.Client
	keys    *oidc.KeySet
	keysURI string
}

func newSSOState() *ssoState {
	return &ssoState{
		flows: map[string]*ssoFlow{},
		cfg:   ssoProvider{Mode: "auto", Scopes: "openid profile email"},
		hc:    &http.Client{Timeout: 10 * time.Second},
	}
}

// ---------------------------------------------------------------- 配置持久化

func (s *Server) loadSSO() error {
	s.sso.mu.Lock()
	defer s.sso.mu.Unlock()
	data, err := os.ReadFile(ssoFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var cfg ssoProvider
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("解析 %s 失败: %w", ssoFile, err)
	}
	if cfg.Mode == "" {
		cfg.Mode = "auto"
	}
	if cfg.Scopes == "" {
		cfg.Scopes = "openid profile email"
	}
	s.sso.cfg = cfg
	return nil
}

func (s *Server) saveSSO(cfg ssoProvider) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll("data", 0o755); err != nil {
		return err
	}
	tmp := ssoFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ssoFile)
}

// ssoConfig 返回配置副本。
func (s *Server) ssoConfig() ssoProvider {
	s.sso.mu.Lock()
	defer s.sso.mu.Unlock()
	cfg := s.sso.cfg
	cfg.Bindings = append([]ssoBinding(nil), cfg.Bindings...)
	return cfg
}

// ---------------------------------------------------------------- 流程辅助

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// pkce 生成 verifier 与 S256 challenge。
func pkce() (verifier, challenge string) {
	verifier = base64.RawURLEncoding.EncodeToString([]byte(randToken(32)))
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

// callbackURL 计算回调地址：优先配置，其次按请求推断。
func (s *Server) callbackURL(r *http.Request, cfg ssoProvider) string {
	if cfg.RedirectURL != "" {
		return cfg.RedirectURL
	}
	return s.baseFor(r) + "/admin/sso/callback"
}

// identity 一次登录解析出的身份。
type identity struct {
	Issuer string
	Sub    string
	Label  string
	Mode   string
}

// resolveIdentity 根据令牌响应判定 OIDC / OAuth 并解析身份。
func (s *Server) resolveIdentity(ctx context.Context, r *http.Request, cfg ssoProvider, flow *ssoFlow, tr *oidc.TokenResponse) (*identity, error) {
	issuer := cfg.Issuer
	if issuer == "" {
		issuer = cfg.AuthorizationEndpoint
	}
	// 显式或自动判定：有 id_token 且配了 jwks 就按 OIDC 校验。
	mode := cfg.Mode
	if mode == "" {
		mode = "auto"
	}
	useJWT := tr.IDToken != "" && (mode == "oidc" || (mode == "auto" && cfg.JWKSUri != ""))
	if useJWT {
		ks, err := s.ssoKeySet(cfg)
		if err != nil {
			return nil, err
		}
		claims, err := oidc.VerifyJWT(ctx, tr.IDToken, ks, cfg.Issuer, cfg.ClientID, flow.nonce)
		if err != nil {
			return nil, fmt.Errorf("id_token 校验失败: %w", err)
		}
		label := claims.Email
		if label == "" {
			label = claims.PreferredUsername
		}
		if label == "" {
			label = claims.Name
		}
		if claims.Sub == "" {
			return nil, errors.New("id_token 缺少 sub")
		}
		return &identity{Issuer: claims.Iss, Sub: claims.Sub, Label: label, Mode: "oidc"}, nil
	}

	// OAuth2：用 userinfo 取身份
	ui, err := oidc.UserInfo(ctx, s.sso.hc, cfg.UserinfoEndpoint, tr.AccessToken)
	if err != nil {
		return nil, err
	}
	sub := firstString(ui, "sub", "id", "username", "preferred_username", "email", "login")
	if sub == "" {
		return nil, errors.New("userinfo 里找不到可用的用户标识（sub/id/username/email）")
	}
	iss := issuer
	if v := firstString(ui, "iss"); v != "" {
		iss = v
	}
	label := firstString(ui, "email", "name", "preferred_username", "username")
	return &identity{Issuer: iss, Sub: sub, Label: label, Mode: "oauth"}, nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
			if f, ok := v.(float64); ok {
				return fmt.Sprintf("%.0f", f)
			}
		}
	}
	return ""
}

// ssoKeySet 按 jwks_uri 复用 KeySet。
func (s *Server) ssoKeySet(cfg ssoProvider) (*oidc.KeySet, error) {
	if cfg.JWKSUri == "" {
		return nil, errors.New("未配置 jwks_uri，无法校验 id_token")
	}
	s.sso.mu.Lock()
	defer s.sso.mu.Unlock()
	if s.sso.keys == nil || s.sso.keysURI != cfg.JWKSUri {
		s.sso.keys = oidc.NewKeySet(s.sso.hc, cfg.JWKSUri)
		s.sso.keysURI = cfg.JWKSUri
	}
	return s.sso.keys, nil
}

// ---------------------------------------------------------------- 处理器

// handleSSOLogin 开始授权码流程。?bind=1 表示把身份绑定到当前会话。
func (s *Server) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
	cfg := s.ssoConfig()
	bind := r.URL.Query().Get("bind") == "1"
	if !cfg.Enabled && !bind {
		http.Error(w, "未启用 SSO", http.StatusNotFound)
		return
	}
	if bind && !s.sessionValid(r) {
		http.Error(w, "请先用密码登录后台，再绑定 SSO 账号", http.StatusUnauthorized)
		return
	}
	if cfg.AuthorizationEndpoint == "" || cfg.ClientID == "" {
		http.Error(w, "SSO 未配置完整（缺少授权端点或 client_id）", http.StatusBadRequest)
		return
	}

	verifier, challenge := pkce()
	state := randToken(24)
	nonce := randToken(16)

	s.sso.mu.Lock()
	// 顺手清理过期流程
	for k, f := range s.sso.flows {
		if time.Now().After(f.expire) {
			delete(s.sso.flows, k)
		}
	}
	s.sso.flows[state] = &ssoFlow{verifier: verifier, nonce: nonce, bind: bind, expire: time.Now().Add(10 * time.Minute)}
	s.sso.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name: "ecy_sso_state", Value: state, Path: "/admin", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 600, Secure: requestIsHTTPS(r),
	})

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", s.callbackURL(r, cfg))
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	scope := cfg.Scopes
	if scope == "" {
		scope = "openid profile email"
	}
	q.Set("scope", scope)
	authURL := cfg.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleSSOCallback 处理回调：换令牌 → 判定模式 → 校验身份 → 绑定或建会话。
func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	cfg := s.ssoConfig()
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.ssoError(w, "授权失败: "+e+" "+q.Get("error_description"))
		return
	}
	state := q.Get("state")
	code := q.Get("code")
	if state == "" || code == "" {
		s.ssoError(w, "回调缺少 state 或 code")
		return
	}
	// 校验 state 与会话 cookie
	c, err := r.Cookie("ecy_sso_state")
	if err != nil || c.Value != state {
		s.ssoError(w, "state 校验失败（会话过期或跨站请求）")
		return
	}
	s.sso.mu.Lock()
	flow := s.sso.flows[state]
	delete(s.sso.flows, state)
	s.sso.mu.Unlock()
	if flow == nil || time.Now().After(flow.expire) {
		s.ssoError(w, "登录流程已过期，请重试")
		return
	}

	tr, err := oidc.ExchangeCode(r.Context(), s.sso.hc, &oidc.Provider{TokenEndpoint: cfg.TokenEndpoint,
		TokenEndpointAuthMethodsSupported: nil}, cfg.ClientID, cfg.ClientSecret, code,
		s.callbackURL(r, cfg), flow.verifier)
	if err != nil {
		s.ssoError(w, err.Error())
		return
	}

	id, err := s.resolveIdentity(r.Context(), r, cfg, flow, tr)
	if err != nil {
		s.ssoError(w, err.Error())
		return
	}

	// 记录判定出的模式，便于后台展示
	s.sso.mu.Lock()
	s.sso.cfg.DetectedMode = id.Mode
	s.sso.mu.Unlock()

	if flow.bind {
		if !s.sessionValid(r) {
			s.ssoError(w, "绑定需要先登录后台")
			return
		}
		s.bindSSOIdentity(id)
		http.Redirect(w, r, "/admin?sso_bound=1", http.StatusFound)
		return
	}

	if !s.isBound(id) {
		s.ssoError(w, fmt.Sprintf("该账号未绑定：%s（%s）。请先用密码登录后台，在「登录与 SSO」里点「绑定当前账号」。", id.Label, id.Sub))
		return
	}
	s.sessionStart(w, r)
	http.Redirect(w, r, "/admin", http.StatusFound)
}

// ssoError 输出一个简单的错误页（不泄露内部细节以外的东西）。
func (s *Server) ssoError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>SSO 登录失败</title>
<body style="font-family:system-ui;max-width:640px;margin:80px auto;line-height:1.7">
<h2>SSO 登录失败</h2><p>%s</p><p><a href="/admin">返回登录页</a></p></body>`, html.EscapeString(msg))
}

// bindSSOIdentity 绑定身份（同 iss+sub 去重）。
func (s *Server) bindSSOIdentity(id *identity) {
	s.sso.mu.Lock()
	defer s.sso.mu.Unlock()
	for _, b := range s.sso.cfg.Bindings {
		if b.Issuer == id.Issuer && b.Sub == id.Sub {
			return
		}
	}
	s.sso.cfg.Bindings = append(s.sso.cfg.Bindings, ssoBinding{
		Issuer: id.Issuer, Sub: id.Sub, Label: id.Label,
		BoundAt: time.Now().Format(time.RFC3339),
	})
	_ = s.saveSSOLocked()
}

func (s *Server) isBound(id *identity) bool {
	cfg := s.ssoConfig()
	for _, b := range cfg.Bindings {
		if b.Issuer == id.Issuer && b.Sub == id.Sub {
			return true
		}
	}
	return false
}

// saveSSOLocked 在持锁状态下落盘（内部会再次取锁，改用直接写）。
func (s *Server) saveSSOLocked() error {
	b, err := json.MarshalIndent(s.sso.cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll("data", 0o755); err != nil {
		return err
	}
	return os.WriteFile(ssoFile, b, 0o600)
}

// handleSSOConfig 保存配置。
func (s *Server) handleSSOConfig(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	cfg := s.ssoConfig()
	cfg.Enabled = r.PostFormValue("enabled") == "1"
	cfg.SSOOnly = r.PostFormValue("sso_only") == "1"
	cfg.Issuer = strings.TrimSpace(r.PostFormValue("issuer"))
	cfg.AuthorizationEndpoint = strings.TrimSpace(r.PostFormValue("authorization_endpoint"))
	cfg.TokenEndpoint = strings.TrimSpace(r.PostFormValue("token_endpoint"))
	cfg.UserinfoEndpoint = strings.TrimSpace(r.PostFormValue("userinfo_endpoint"))
	cfg.JWKSUri = strings.TrimSpace(r.PostFormValue("jwks_uri"))
	cfg.ClientID = strings.TrimSpace(r.PostFormValue("client_id"))
	if v := r.PostFormValue("client_secret"); v != "" {
		cfg.ClientSecret = strings.TrimSpace(v)
	}
	cfg.Scopes = strings.TrimSpace(r.PostFormValue("scopes"))
	cfg.RedirectURL = strings.TrimSpace(r.PostFormValue("redirect_url"))
	switch m := r.PostFormValue("mode"); m {
	case "oidc", "oauth", "auto":
		cfg.Mode = m
	default:
		cfg.Mode = "auto"
	}
	if cfg.Enabled && (cfg.AuthorizationEndpoint == "" || cfg.TokenEndpoint == "" || cfg.ClientID == "") {
		s.adminResult(w, "启用 SSO 需要至少填写授权端点、token 端点与 client_id")
		return
	}
	if cfg.SSOOnly && len(cfg.Bindings) == 0 {
		s.adminResult(w, "开启「仅使用 SSO」前，请先绑定至少一个 SSO 账号，否则会把自己锁在门外")
		return
	}
	s.sso.mu.Lock()
	s.sso.cfg = cfg
	err := s.saveSSOLocked()
	s.sso.mu.Unlock()
	if err != nil {
		s.adminResult(w, "保存失败: "+err.Error())
		return
	}
	s.adminResult(w, "SSO 配置已保存")
}

// handleSSODiscover 拉取发现文档，返回给前端自动填充。
func (s *Server) handleSSODiscover(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("discovery"))
	p, err := oidc.Discover(r.Context(), s.sso.hc, raw)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "issuer": p.Issuer,
		"authorization_endpoint": p.AuthorizationEndpoint,
		"token_endpoint":         p.TokenEndpoint,
		"userinfo_endpoint":      p.UserinfoEndpoint,
		"jwks_uri":               p.JWKSUri,
		"mode":                   p.Mode(),
		"scopes":                 strings.Join(p.ScopesSupported, " "),
		"algs":                   p.IDTokenSigningAlgValuesSupported,
		"auth_methods":           p.TokenEndpointAuthMethodsSupported,
	})
}

// handleSSOUnbind 解除绑定。
func (s *Server) handleSSOUnbind(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	iss := r.PostFormValue("issuer")
	sub := r.PostFormValue("sub")
	s.sso.mu.Lock()
	out := s.sso.cfg.Bindings[:0]
	for _, b := range s.sso.cfg.Bindings {
		if b.Issuer == iss && b.Sub == sub {
			continue
		}
		out = append(out, b)
	}
	s.sso.cfg.Bindings = out
	if len(out) == 0 {
		s.sso.cfg.SSOOnly = false // 没有绑定就不能强制 SSO，避免锁死
	}
	err := s.saveSSOLocked()
	s.sso.mu.Unlock()
	if err != nil {
		s.adminResult(w, "保存失败: "+err.Error())
		return
	}
	s.adminResult(w, "已解除绑定")
}
