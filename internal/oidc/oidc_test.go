package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signRS256 生成 RS256 token。
func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid})
	c, _ := json.Marshal(claims)
	input := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, 5 /*crypto.SHA256*/, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + b64(sig)
}

// signES256 生成 ES256 token（签名为 r||s 定长拼接）。
func signES256(t *testing.T, key *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]any{"alg": "ES256", "typ": "JWT", "kid": kid})
	c, _ := json.Marshal(claims)
	input := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sz := (key.Curve.Params().BitSize + 7) / 8
	sig := make([]byte, sz*2)
	r.FillBytes(sig[:sz])
	s.FillBytes(sig[sz:])
	return input + "." + b64(sig)
}

func jwksServer(t *testing.T, rsaPub *rsa.PublicKey, ecPub *ecdsa.PublicKey) *httptest.Server {
	t.Helper()
	keys := []map[string]any{}
	if rsaPub != nil {
		keys = append(keys, map[string]any{
			"kty": "RSA", "kid": "rsa1", "use": "sig", "alg": "RS256",
			"n": b64(rsaPub.N.Bytes()), "e": b64(big.NewInt(int64(rsaPub.E)).Bytes()),
		})
	}
	if ecPub != nil {
		keys = append(keys, map[string]any{
			"kty": "EC", "kid": "ec1", "use": "sig", "alg": "ES256", "crv": "P-256",
			"x": b64(ecPub.X.Bytes()), "y": b64(ecPub.Y.Bytes()),
		})
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
}

func baseClaims(nonce string) map[string]any {
	return map[string]any{
		"iss": "https://issuer.example", "sub": "user-1", "aud": "client-1",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"nonce": nonce, "email": "a@b.c", "name": "HINS",
	}
}

// TestVerifyJWT_RS256 RSA 全量算法应可校验。
func TestVerifyJWT_RS256(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := jwksServer(t, &key.PublicKey, nil)
	defer srv.Close()
	ks := NewKeySet(srv.Client(), srv.URL)
	tok := signRS256(t, key, "rsa1", baseClaims("n1"))
	c, err := VerifyJWT(context.Background(), tok, ks, "https://issuer.example", "client-1", "n1")
	if err != nil || c.Sub != "user-1" {
		t.Fatalf("校验失败: %+v, %v", c, err)
	}
	// nonce 不符
	if _, err := VerifyJWT(context.Background(), tok, ks, "https://issuer.example", "client-1", "other"); err == nil {
		t.Fatal("nonce 不符应失败")
	}
	// aud 不符
	if _, err := VerifyJWT(context.Background(), tok, ks, "https://issuer.example", "client-2", "n1"); err == nil {
		t.Fatal("aud 不符应失败")
	}
	// 过期
	expired := baseClaims("n1")
	expired["exp"] = time.Now().Add(-time.Hour).Unix()
	if _, err := VerifyJWT(context.Background(), signRS256(t, key, "rsa1", expired), ks, "https://issuer.example", "client-1", "n1"); err == nil {
		t.Fatal("过期应失败")
	}
}

// TestVerifyJWT_ES256 EC 曲线应可校验。
func TestVerifyJWT_ES256(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := jwksServer(t, nil, &key.PublicKey)
	defer srv.Close()
	ks := NewKeySet(srv.Client(), srv.URL)
	tok := signES256(t, key, "ec1", baseClaims("n2"))
	if _, err := VerifyJWT(context.Background(), tok, ks, "https://issuer.example", "client-1", "n2"); err != nil {
		t.Fatalf("ES256 校验失败: %v", err)
	}
	// 篡改签名
	bad := tok[:len(tok)-4] + "AAAA"
	if _, err := VerifyJWT(context.Background(), bad, ks, "https://issuer.example", "client-1", "n2"); err == nil {
		t.Fatal("篡改签名应失败")
	}
}

// TestRejectAlgNone alg=none 必须拒绝。
func TestRejectAlgNone(t *testing.T) {
	h, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	c, _ := json.Marshal(baseClaims(""))
	tok := b64(h) + "." + b64(c) + "."
	srv := jwksServer(t, nil, nil)
	defer srv.Close()
	if _, err := VerifyJWT(context.Background(), tok, NewKeySet(srv.Client(), srv.URL), "", "", ""); err == nil {
		t.Fatal("alg=none 应被拒绝")
	}
}

// TestDiscover 发现文档解析与模式判定。
func TestDiscover(t *testing.T) {
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/auth",
			"token_endpoint": issuer + "/token", "userinfo_endpoint": issuer + "/userinfo",
			"jwks_uri": issuer + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256", "ES256"},
		})
	}))
	defer srv.Close()
	issuer = srv.URL
	p, err := Discover(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("发现失败: %v", err)
	}
	if p.AuthorizationEndpoint != issuer+"/auth" || p.Mode() != "oidc" {
		t.Fatalf("解析结果不对: %+v", p)
	}
	// 只填 issuer 也能补全路径
	if DiscoveryURL(srv.URL) != srv.URL+"/.well-known/openid-configuration" {
		t.Fatal("发现地址补全失败")
	}
	// 没有 jwks 的纯 OAuth 文档应判定为 oauth
	p2 := &Provider{AuthorizationEndpoint: "x", TokenEndpoint: "y"}
	if p2.Mode() != "oauth" {
		t.Fatal("无 jwks 应判定为 oauth")
	}
}
