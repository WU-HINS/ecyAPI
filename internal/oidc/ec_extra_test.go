package oidc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestEdDSA OKP/Ed25519（EdDSA）应可校验。
func TestEdDSA(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv := serveJWKS(t, []map[string]any{{
		"kty": "OKP", "kid": "ed1", "crv": "Ed25519", "alg": "EdDSA",
		"x": b64(pub),
	}})
	ks := NewKeySet(srv.Client(), srv.URL)

	h, _ := json.Marshal(map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": "ed1"})
	c, _ := json.Marshal(baseClaims("n"))
	input := b64(h) + "." + b64(c)
	tok := input + "." + b64(ed25519.Sign(priv, []byte(input)))

	if _, err := VerifyJWT(context.Background(), tok, ks, "https://issuer.example", "client-1", "n"); err != nil {
		t.Fatalf("EdDSA 校验失败: %v", err)
	}
	// 篡改应失败
	if _, err := VerifyJWT(context.Background(), input+"."+b64(make([]byte, 64)), ks, "", "", "n"); err == nil {
		t.Fatal("EdDSA 篡改应失败")
	}
}

// TestJWKS_X5c 只发布证书（x5c）的 JWKS 也要能用。
func TestJWKS_X5c(t *testing.T) {
	key, _ := ecdsaGenerate(t)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "idp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	srv := serveJWKS(t, []map[string]any{{
		"kty": "EC", "kid": "cert", "alg": "ES256", "crv": "P-256",
		"x5c": []string{base64.StdEncoding.EncodeToString(der)},
	}})
	ks := NewKeySet(srv.Client(), srv.URL)
	tok := signEC(t, key, "ES256", "cert", baseClaims("n"))
	if _, err := VerifyJWT(context.Background(), tok, ks, "https://issuer.example", "client-1", "n"); err != nil {
		t.Fatalf("x5c 校验失败: %v", err)
	}
}

// TestNoKidNoAlg JWKS 既没 kid 也没 alg 时，靠验签选键。
func TestNoKidNoAlg(t *testing.T) {
	key, _ := ecdsaGenerate(t)
	other, _ := ecdsaGenerate(t)
	srv := serveJWKS(t, []map[string]any{
		{"kty": "EC", "crv": "P-256", "x": b64(other.PublicKey.X.Bytes()), "y": b64(other.PublicKey.Y.Bytes())},
		{"kty": "EC", "crv": "P-256", "x": b64(key.PublicKey.X.Bytes()), "y": b64(key.PublicKey.Y.Bytes())},
	})
	ks := NewKeySet(srv.Client(), srv.URL)
	tok := signEC(t, key, "ES256", "", baseClaims("n"))
	if _, err := VerifyJWT(context.Background(), tok, ks, "https://issuer.example", "client-1", "n"); err != nil {
		t.Fatalf("无 kid/alg 的 JWKS 校验失败: %v", err)
	}
}

// TestDiscoverMarksOAuth 纯 OAuth 发现文档（无 jwks）应判定 oauth。
func TestDiscoverMarksOAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint": "https://x/a", "token_endpoint": "https://x/t",
			"userinfo_endpoint": "https://x/u",
		})
	}))
	defer srv.Close()
	p, err := Discover(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode() != "oauth" {
		t.Fatalf("应判定为 oauth，得到 %s", p.Mode())
	}
}
