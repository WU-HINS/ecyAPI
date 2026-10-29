package server

// 白名单核验：穷举「能不能绕过绑定」的所有路径。
// 每个子测试都断言：既没有拿到会话，也没有留下新的绑定。

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ecyapi/internal/config"
)

// newSSOServer 造一个已登录、已配置好 SSO 的实例，返回 server 与管理员会话。
func newSSOServer(t *testing.T, idp *mockIdP) (*Server, *http.Cookie) {
	t.Helper()
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: filepath.Join(dir, "categories"), AdminPassword: "pw", PreviewMP: 16})
	if err != nil {
		t.Fatal(err)
	}
	session := login(t, s, "pw")
	if session == nil {
		t.Fatal("密码登录失败")
	}
	postAdmin(t, s, session, "/admin/sso/config", url.Values{
		"enabled": {"1"}, "client_id": {"client-1"}, "client_secret": {"sec"},
		"issuer": {idp.issuer}, "authorization_endpoint": {idp.issuer + "/auth"},
		"token_endpoint": {idp.issuer + "/token"}, "jwks_uri": {idp.issuer + "/jwks"},
		"userinfo_endpoint": {idp.issuer + "/userinfo"}, "mode": {"auto"},
	})
	return s, session
}

func hasSessionCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "ecy_admin" && c.Value != "" {
			return true
		}
	}
	return false
}

// TestSSOWhitelist 逐条验证：未绑定的身份无法登录，绕过路径全部失效。
func TestSSOWhitelist(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.srv.Close()
	s, adminSession := newSSOServer(t, idp)

	// 先绑定一个账号（iss + sub = u-1）
	loc, sc := startSSO(t, s, adminSession, true)
	idp.mu.Lock()
	idp.nonces["bind-code"] = loc.Query().Get("nonce")
	idp.mu.Unlock()
	if rec := ssoCallback(t, s, adminSession, sc, "bind-code"); rec.Code != http.StatusFound {
		t.Fatalf("绑定失败: %d", rec.Code)
	}

	t.Run("未绑定身份被拒", func(t *testing.T) {
		idp.mu.Lock()
		idp.sub = "attacker"
		idp.mu.Unlock()
		defer func() { idp.mu.Lock(); idp.sub = "u-1"; idp.mu.Unlock() }()

		loc, sc := startSSO(t, s, nil, false)
		idp.mu.Lock()
		idp.nonces["c1"] = loc.Query().Get("nonce")
		idp.mu.Unlock()
		rec := ssoCallback(t, s, nil, sc, "c1")
		if rec.Code != http.StatusForbidden || hasSessionCookie(rec) {
			t.Fatalf("未绑定身份必须 403 且无会话: %d", rec.Code)
		}
	})

	t.Run("没有 state cookie 一律拒绝", func(t *testing.T) {
		loc, _ := startSSO(t, s, nil, false)
		idp.mu.Lock()
		idp.nonces["c2"] = loc.Query().Get("nonce")
		idp.mu.Unlock()
		req := httptest.NewRequest("GET", "/admin/sso/callback?code=c2&state="+loc.Query().Get("state"), nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req) // 不带 ecy_sso_state
		if rec.Code != http.StatusForbidden || hasSessionCookie(rec) {
			t.Fatalf("缺少 state cookie 必须拒绝: %d", rec.Code)
		}
	})

	t.Run("伪造 state 拒绝", func(t *testing.T) {
		loc, sc := startSSO(t, s, nil, false)
		idp.mu.Lock()
		idp.nonces["c3"] = loc.Query().Get("nonce")
		idp.mu.Unlock()
		req := httptest.NewRequest("GET", "/admin/sso/callback?code=c3&state=forged", nil)
		req.AddCookie(sc)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || hasSessionCookie(rec) {
			t.Fatalf("伪造 state 必须拒绝: %d", rec.Code)
		}
	})

	t.Run("state 重放拒绝", func(t *testing.T) {
		loc, sc := startSSO(t, s, nil, false)
		idp.mu.Lock()
		idp.nonces["c4"] = loc.Query().Get("nonce")
		idp.mu.Unlock()
		if rec := ssoCallback(t, s, nil, sc, "c4"); rec.Code != http.StatusFound {
			t.Fatalf("首次登录应成功: %d", rec.Code)
		}
		rec := ssoCallback(t, s, nil, sc, "c4") // 同 state 再用一次
		if rec.Code != http.StatusForbidden || hasSessionCookie(rec) {
			t.Fatalf("重放必须拒绝: %d", rec.Code)
		}
	})

	t.Run("流程过期拒绝", func(t *testing.T) {
		loc, sc := startSSO(t, s, nil, false)
		state := loc.Query().Get("state")
		idp.mu.Lock()
		idp.nonces["c5"] = loc.Query().Get("nonce")
		idp.mu.Unlock()
		s.sso.mu.Lock()
		s.sso.flows[state].expire = time.Now().Add(-time.Minute)
		s.sso.mu.Unlock()
		rec := ssoCallback(t, s, nil, sc, "c5")
		if rec.Code != http.StatusForbidden || hasSessionCookie(rec) {
			t.Fatalf("过期流程必须拒绝: %d", rec.Code)
		}
	})

	t.Run("伪造签名的 token 拒绝", func(t *testing.T) {
		// IdP 用另一把私钥签名，JWKS 里仍是原公钥
		other, _ := rsa.GenerateKey(rand.Reader, 2048)
		idp.mu.Lock()
		orig := idp.key
		idp.key = other
		idp.mu.Unlock()
		defer func() { idp.mu.Lock(); idp.key = orig; idp.mu.Unlock() }()

		loc, sc := startSSO(t, s, nil, false)
		idp.mu.Lock()
		idp.nonces["c6"] = loc.Query().Get("nonce")
		idp.mu.Unlock()
		rec := ssoCallback(t, s, nil, sc, "c6")
		if rec.Code != http.StatusForbidden || hasSessionCookie(rec) {
			t.Fatalf("签名不符必须拒绝: %d", rec.Code)
		}
	})

	t.Run("alg=none 拒绝", func(t *testing.T) {
		loc, sc := startSSO(t, s, nil, false)
		idp.mu.Lock()
		idp.algNone = true
		idp.nonces["c7"] = loc.Query().Get("nonce")
		idp.mu.Unlock()
		defer func() { idp.mu.Lock(); idp.algNone = false; idp.mu.Unlock() }()
		rec := ssoCallback(t, s, nil, sc, "c7")
		if rec.Code != http.StatusForbidden || hasSessionCookie(rec) {
			t.Fatalf("alg=none 必须拒绝: %d", rec.Code)
		}
	})

	t.Run("aud 不符拒绝", func(t *testing.T) {
		loc, sc := startSSO(t, s, nil, false)
		idp.mu.Lock()
		idp.aud = "someone-else"
		idp.nonces["c8"] = loc.Query().Get("nonce")
		idp.mu.Unlock()
		defer func() { idp.mu.Lock(); idp.aud = ""; idp.mu.Unlock() }()
		rec := ssoCallback(t, s, nil, sc, "c8")
		if rec.Code != http.StatusForbidden || hasSessionCookie(rec) {
			t.Fatalf("aud 不符必须拒绝: %d", rec.Code)
		}
	})

	t.Run("sub 为空拒绝", func(t *testing.T) {
		loc, sc := startSSO(t, s, nil, false)
		idp.mu.Lock()
		idp.sub = ""
		idp.nonces["c9"] = loc.Query().Get("nonce")
		idp.mu.Unlock()
		defer func() { idp.mu.Lock(); idp.sub = "u-1"; idp.mu.Unlock() }()
		rec := ssoCallback(t, s, nil, sc, "c9")
		if rec.Code != http.StatusForbidden || hasSessionCookie(rec) {
			t.Fatalf("空 sub 必须拒绝: %d", rec.Code)
		}
	})

	t.Run("无会话不能绑定", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/admin/sso/login?bind=1", nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("未登录绑定应 401: %d", rec.Code)
		}
	})

	t.Run("同 sub 不同 issuer 不匹配", func(t *testing.T) {
		// 把 issuer 改成另一个，token 的 iss 也随之变化 → 绑定不再匹配
		cfg := s.ssoConfig()
		cfg.Issuer = idp.issuer + "/other"
		s.sso.mu.Lock()
		s.sso.cfg = cfg
		s.sso.mu.Unlock()
		defer func() {
			cfg.Issuer = idp.issuer
			s.sso.mu.Lock()
			s.sso.cfg = cfg
			s.sso.mu.Unlock()
		}()
		loc, sc := startSSO(t, s, nil, false)
		idp.mu.Lock()
		idp.nonces["c10"] = loc.Query().Get("nonce")
		idp.mu.Unlock()
		rec := ssoCallback(t, s, nil, sc, "c10")
		if rec.Code != http.StatusForbidden || hasSessionCookie(rec) {
			t.Fatalf("issuer 不同不应命中绑定: %d", rec.Code)
		}
	})

	// 所有失败尝试都不应留下新绑定：白名单里只能有最初那一条。
	if n := len(s.ssoConfig().Bindings); n != 1 {
		t.Fatalf("绑定数量应始终为 1，实际 %d：%+v", n, s.ssoConfig().Bindings)
	}
}
