package oidc

import (
	"context"
	"crypto"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Provider 发现文档里我们关心的字段。
type Provider struct {
	Issuer                            string         `json:"issuer"`
	AuthorizationEndpoint             string         `json:"authorization_endpoint"`
	TokenEndpoint                     string         `json:"token_endpoint"`
	UserinfoEndpoint                  string         `json:"userinfo_endpoint"`
	JWKSUri                           string         `json:"jwks_uri"`
	IDTokenSigningAlgValuesSupported  []string       `json:"id_token_signing_alg_values_supported"`
	ScopesSupported                   []string       `json:"scopes_supported"`
	ResponseTypesSupported            []string       `json:"response_types_supported"`
	GrantTypesSupported               []string       `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string       `json:"token_endpoint_auth_methods_supported"`
	Raw                               map[string]any `json:"-"`
}

// Mode 判断该 provider 更像 OIDC 还是纯 OAuth2。
// 依据：有 jwks_uri / userinfo、且响应类型包含 code（OIDC 用 id_token）。
func (p *Provider) Mode() string {
	if p.JWKSUri != "" || len(p.IDTokenSigningAlgValuesSupported) > 0 {
		return "oidc"
	}
	return "oauth"
}

// DiscoveryURL 把用户填的地址规范成发现文档地址。
// 支持直接填 issuer（自动补 /.well-known/openid-configuration）。
func DiscoveryURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "/.well-known/") {
		return raw
	}
	return strings.TrimRight(raw, "/") + "/.well-known/openid-configuration"
}

// Discover 拉取发现文档。
func Discover(ctx context.Context, hc *http.Client, raw string) (*Provider, error) {
	u := DiscoveryURL(raw)
	if u == "" {
		return nil, fmt.Errorf("oidc: 请填写发现端点或 issuer")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: 拉取发现文档失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: 发现端点返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var p Provider
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("oidc: 发现文档不是合法 JSON: %w", err)
	}
	if p.AuthorizationEndpoint == "" || p.TokenEndpoint == "" {
		return nil, fmt.Errorf("oidc: 发现文档缺少 authorization_endpoint 或 token_endpoint")
	}
	_ = json.Unmarshal(body, &p.Raw)
	return &p, nil
}

// TokenResponse 令牌端点返回。
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	IDToken     string `json:"id_token"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// ExchangeCode 授权码换令牌。secret 为空表示公共客户端。
// 优先 client_secret_basic；若发现文档声明只支持 client_secret_post 则用 post。
func ExchangeCode(ctx context.Context, hc *http.Client, p *Provider, clientID, clientSecret, code, redirectURI, verifier string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if clientSecret != "" && !prefersPostAuth(p) {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	} else if clientSecret != "" {
		form.Set("client_secret", clientSecret)
		req.Body = io.NopCloser(strings.NewReader(form.Encode()))
		req.ContentLength = int64(len(form.Encode()))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: 换取令牌失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("oidc: 令牌端点返回 %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("oidc: 令牌响应不是合法 JSON: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("oidc: 令牌响应缺少 access_token")
	}
	return &tr, nil
}

func prefersPostAuth(p *Provider) bool {
	hasBasic, hasPost := false, false
	for _, m := range p.TokenEndpointAuthMethodsSupported {
		switch m {
		case "client_secret_basic":
			hasBasic = true
		case "client_secret_post":
			hasPost = true
		}
	}
	return hasPost && !hasBasic
}

// UserInfo 拉取用户信息（OAuth2 模式下的身份来源）。
func UserInfo(ctx context.Context, hc *http.Client, endpoint, accessToken string) (map[string]any, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("oidc: 未配置 userinfo 端点")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: 拉取 userinfo 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("oidc: userinfo 返回 %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("oidc: userinfo 不是合法 JSON: %w", err)
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// KeySet 缓存 JWKS 公钥，kid 未命中时按节流重新拉取。
type KeySet struct {
	mu      sync.RWMutex
	uri     string
	hc      *http.Client
	keys    map[string]crypto.PublicKey
	byAlg   map[string]crypto.PublicKey
	all     []crypto.PublicKey // 全部公钥（JWKS 可能既无 kid 也无 alg）
	fetched time.Time
}

// NewKeySet 创建 JWKS 缓存。
func NewKeySet(hc *http.Client, uri string) *KeySet {
	return &KeySet{uri: uri, hc: hc, keys: map[string]crypto.PublicKey{}, byAlg: map[string]crypto.PublicKey{}}
}

// Refresh 拉取并替换公钥。
func (k *KeySet) Refresh(ctx context.Context) error {
	if k.uri == "" {
		return fmt.Errorf("oidc: 未配置 jwks_uri")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.uri, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := k.hc.Do(req)
	if err != nil {
		return fmt.Errorf("oidc: 拉取 JWKS 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("oidc: JWKS 返回 %d", resp.StatusCode)
	}
	var set JWKS
	if err := json.Unmarshal(body, &set); err != nil {
		return fmt.Errorf("oidc: JWKS 不是合法 JSON: %w", err)
	}
	keys := make(map[string]crypto.PublicKey, len(set.Keys))
	byAlg := make(map[string]crypto.PublicKey, len(set.Keys))
	all := make([]crypto.PublicKey, 0, len(set.Keys))
	for _, jk := range set.Keys {
		if jk.Use != "" && jk.Use != "sig" {
			continue
		}
		pub, err := jk.PublicKey()
		if err != nil {
			continue
		}
		if jk.Kid != "" {
			keys[jk.Kid] = pub
		}
		if jk.Alg != "" {
			byAlg[jk.Alg] = pub
		}
		all = append(all, pub)
	}
	if len(all) == 0 {
		return fmt.Errorf("oidc: JWKS 中没有可用的签名公钥")
	}
	k.mu.Lock()
	k.keys, k.byAlg, k.all, k.fetched = keys, byAlg, all, time.Now()
	k.mu.Unlock()
	return nil
}

// Key 取公钥：先按 kid，找不到按 alg；都没有则（节流地）刷新一次。
func (k *KeySet) Key(ctx context.Context, kid, alg string) (crypto.PublicKey, error) {
	get := func() crypto.PublicKey {
		k.mu.RLock()
		defer k.mu.RUnlock()
		if kid != "" {
			if pk, ok := k.keys[kid]; ok {
				return pk
			}
		}
		if alg != "" {
			return k.byAlg[alg]
		}
		return nil
	}
	if pk := get(); pk != nil {
		return pk, nil
	}
	k.mu.RLock()
	last := k.fetched
	k.mu.RUnlock()
	if time.Since(last) < 30*time.Second {
		return nil, fmt.Errorf("oidc: 找不到匹配的公钥（kid=%q alg=%q）", kid, alg)
	}
	if err := k.Refresh(ctx); err != nil {
		return nil, err
	}
	if pk := get(); pk != nil {
		return pk, nil
	}
	return nil, fmt.Errorf("oidc: 找不到匹配的公钥（kid=%q alg=%q）", kid, alg)
}

// Candidates 返回所有候选公钥（kid/alg 都对不上时逐个尝试）。
// 有些 IdP 的 JWKS 既没有 kid 也没有 alg，只能靠「签名能验通」来选键。
func (k *KeySet) Candidates() []crypto.PublicKey {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]crypto.PublicKey, len(k.all))
	copy(out, k.all)
	return out
}

// VerifyJWT 校验 id_token：签名（RS/PS/ES/EdDSA）+ iss/aud/exp/nonce。
func VerifyJWT(ctx context.Context, token string, ks *KeySet, issuer, clientID, nonce string) (*Claims, error) {
	kid, alg, err := KeyID(token)
	if err != nil {
		return nil, err
	}
	sig, err := Signature(token)
	if err != nil {
		return nil, err
	}
	input := SigningInput(token)

	if pk, kerr := ks.Key(ctx, kid, alg); kerr == nil {
		if err := VerifySignature(alg, pk, input, sig); err != nil {
			return nil, err
		}
	} else {
		// 兜底：kid/alg 对不上（或 JWKS 没写 kid），逐个候选验证签名。
		verified := false
		for _, cand := range ks.Candidates() {
			if VerifySignature(alg, cand, input, sig) == nil {
				verified = true
				break
			}
		}
		if !verified {
			return nil, kerr
		}
	}
	_, raw, err := ParseUnverified(token)
	if err != nil {
		return nil, err
	}
	c := Claims{Raw: raw}
	c.Iss, _ = raw["iss"].(string)
	c.Sub, _ = raw["sub"].(string)
	c.Nonce, _ = raw["nonce"].(string)
	c.Email, _ = raw["email"].(string)
	c.Name, _ = raw["name"].(string)
	c.PreferredUsername, _ = raw["preferred_username"].(string)
	switch v := raw["aud"].(type) {
	case string:
		c.Aud = []string{v}
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok {
				c.Aud = append(c.Aud, s)
			}
		}
	}
	if v, ok := raw["exp"].(float64); ok {
		c.Exp = int64(v)
	}
	if v, ok := raw["iat"].(float64); ok {
		c.Iat = int64(v)
	}
	if err := CheckClaims(c, issuer, clientID, nonce, time.Now()); err != nil {
		return nil, err
	}
	return &c, nil
}
