package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ecyapi/internal/config"
)

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// mockIdP 一个最小的 OIDC Provider（发现 + JWKS + token + userinfo）。
type mockIdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	nonces map[string]string // code -> nonce
	sub    string
	issuer string
}

func newMockIdP(t *testing.T) *mockIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	m := &mockIdP{key: key, nonces: map[string]string{}, sub: "u-1"}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": m.issuer, "authorization_endpoint": m.issuer + "/auth",
				"token_endpoint": m.issuer + "/token", "jwks_uri": m.issuer + "/jwks",
				"userinfo_endpoint":                     m.issuer + "/userinfo",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
				"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
				"n": b64u(key.PublicKey.N.Bytes()),
				"e": b64u(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}}})
		case "/token":
			_ = r.ParseForm()
			code := r.Form.Get("code")
			m.mu.Lock()
			nonce := m.nonces[code]
			sub := m.sub
			m.mu.Unlock()
			claims := map[string]any{
				"iss": m.issuer, "sub": sub, "aud": "client-1",
				"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
				"nonce": nonce, "email": "admin@example.com",
			}
			h, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "k1"})
			c, _ := json.Marshal(claims)
			input := b64u(h) + "." + b64u(c)
			sum := sha256.Sum256([]byte(input))
			sig, _ := rsa.SignPKCS1v15(rand.Reader, key, 5, sum[:])
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at", "token_type": "Bearer", "id_token": input + "." + b64u(sig),
			})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "oauth-user", "email": "o@example.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	m.issuer = m.srv.URL
	return m
}

// authorize 发起 /admin/sso/login，返回授权地址与 state cookie。
func startSSO(t *testing.T, s *Server, session *http.Cookie, bind bool) (*url.URL, *http.Cookie) {
	t.Helper()
	target := "/admin/sso/login"
	if bind {
		target += "?bind=1"
	}
	req := httptest.NewRequest("GET", target, nil)
	if session != nil {
		req.AddCookie(session)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("发起登录应 302，得到 %d: %s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var stateCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "ecy_sso_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("缺少 state cookie")
	}
	return loc, stateCookie
}

// ssoCallback 带着 code/state 回调，返回响应记录器。
func ssoCallback(t *testing.T, s *Server, session, stateCookie *http.Cookie, code string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/admin/sso/callback?code="+code+"&state="+stateCookie.Value, nil)
	req.AddCookie(stateCookie)
	if session != nil {
		req.AddCookie(session)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSSOBindThenLogin(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	idp := newMockIdP(t)
	defer idp.srv.Close()

	s, err := New(config.Config{DataDir: filepath.Join(dir, "categories"), AdminPassword: "pw", PreviewMP: 16})
	if err != nil {
		t.Fatal(err)
	}
	session := login(t, s, "pw")
	if session == nil {
		t.Fatal("密码登录失败")
	}

	// 1) 保存配置（启用 SSO）
	body := postAdmin(t, s, session, "/admin/sso/config", url.Values{
		"enabled": {"1"}, "client_id": {"client-1"}, "client_secret": {"sec"},
		"issuer": {idp.issuer}, "authorization_endpoint": {idp.issuer + "/auth"},
		"token_endpoint": {idp.issuer + "/token"}, "jwks_uri": {idp.issuer + "/jwks"},
		"userinfo_endpoint": {idp.issuer + "/userinfo"}, "scopes": {"openid profile email"}, "mode": {"auto"},
	})
	if !strings.Contains(body, "SSO 配置已保存") {
		t.Fatalf("保存失败: %s", body)
	}

	// 2) 绑定：发起 → 回调
	loc, sc := startSSO(t, s, session, true)
	state := loc.Query().Get("state")
	nonce := loc.Query().Get("nonce")
	code := "code-1"
	idp.mu.Lock()
	idp.nonces[code] = nonce
	idp.mu.Unlock()
	if got := loc.Query().Get("redirect_uri"); !strings.Contains(got, "/admin/sso/callback") {
		t.Fatalf("redirect_uri 不对: %s", got)
	}
	rec := ssoCallback(t, s, session, sc, code)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "sso_bound=1") {
		t.Fatalf("绑定回调失败: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(string(mustRead(t, "data/sso.json")), "u-1") {
		t.Fatal("绑定未写入 sso.json")
	}
	_ = state

	// 3) SSO 登录（未绑定会话）
	loc2, sc2 := startSSO(t, s, nil, false)
	code2 := "code-2"
	idp.mu.Lock()
	idp.nonces[code2] = loc2.Query().Get("nonce")
	idp.mu.Unlock()
	rec = ssoCallback(t, s, nil, sc2, code2)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/admin" {
		t.Fatalf("SSO 登录失败: %d %s", rec.Code, rec.Body.String())
	}
	var hasSession bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "ecy_admin" && c.Value != "" {
			hasSession = true
		}
	}
	if !hasSession {
		t.Fatal("SSO 登录后未建立会话")
	}

	// 4) 未绑定身份必须拒绝
	idp.mu.Lock()
	idp.sub = "intruder"
	idp.mu.Unlock()
	loc3, sc3 := startSSO(t, s, nil, false)
	code3 := "code-3"
	idp.mu.Lock()
	idp.nonces[code3] = loc3.Query().Get("nonce")
	idp.mu.Unlock()
	rec = ssoCallback(t, s, nil, sc3, code3)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "未绑定") {
		t.Fatalf("未绑定身份应被拒绝: %d %s", rec.Code, rec.Body.String())
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	return b
}

// TestSSOOnlyMode 「仅使用 SSO」开启后拒绝密码登录并自动跳转。
func TestSSOOnlyMode(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	_ = os.MkdirAll("data", 0o755)
	idp := newMockIdP(t)
	defer idp.srv.Close()

	s, err := New(config.Config{DataDir: filepath.Join(dir, "categories"), AdminPassword: "pw", PreviewMP: 16})
	if err != nil {
		t.Fatal(err)
	}
	session := login(t, s, "pw")
	postAdmin(t, s, session, "/admin/sso/config", url.Values{
		"enabled": {"1"}, "client_id": {"client-1"},
		"issuer": {idp.issuer}, "authorization_endpoint": {idp.issuer + "/auth"},
		"token_endpoint": {idp.issuer + "/token"}, "jwks_uri": {idp.issuer + "/jwks"},
		"userinfo_endpoint": {idp.issuer + "/userinfo"}, "mode": {"auto"},
	})
	// 未绑定就开 SSO-only：必须被拒绝
	body := postAdmin(t, s, session, "/admin/sso/config", url.Values{
		"enabled": {"1"}, "sso_only": {"1"}, "client_id": {"client-1"},
		"issuer": {idp.issuer}, "authorization_endpoint": {idp.issuer + "/auth"},
		"token_endpoint": {idp.issuer + "/token"}, "jwks_uri": {idp.issuer + "/jwks"},
	})
	if !strings.Contains(body, "请先绑定至少一个 SSO 账号") {
		t.Fatalf("未绑定不应允许开启 SSO-only: %s", body)
	}

	// 绑定一个账号后再开
	loc, sc := startSSO(t, s, session, true)
	idp.mu.Lock()
	idp.nonces["c1"] = loc.Query().Get("nonce")
	idp.mu.Unlock()
	ssoCallback(t, s, session, sc, "c1")
	body = postAdmin(t, s, session, "/admin/sso/config", url.Values{
		"enabled": {"1"}, "sso_only": {"1"}, "client_id": {"client-1"},
		"issuer": {idp.issuer}, "authorization_endpoint": {idp.issuer + "/auth"},
		"token_endpoint": {idp.issuer + "/token"}, "jwks_uri": {idp.issuer + "/jwks"},
	})
	if !strings.Contains(body, "SSO 配置已保存") {
		t.Fatalf("绑定后应能保存: %s", body)
	}

	// 未登录访问后台 → 302 到 SSO
	req := httptest.NewRequest("GET", "/admin", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "/admin/sso/login") {
		t.Fatalf("SSO-only 未登录应跳转 SSO: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// 密码登录被拒绝
	req = httptest.NewRequest("POST", "/admin", strings.NewReader("login_password=pw"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var hasSession bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "ecy_admin" && c.Value != "" {
			hasSession = true
		}
	}
	if hasSession {
		t.Fatal("SSO-only 下密码登录不应成功")
	}
}
