package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
)

func signEC(t *testing.T, key *ecdsa.PrivateKey, alg, kid string, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]any{"alg": alg, "typ": "JWT", "kid": kid})
	c, _ := json.Marshal(claims)
	input := b64(h) + "." + b64(c)
	var digest []byte
	switch alg {
	case "ES384":
		s := sha512.Sum384([]byte(input))
		digest = s[:]
	case "ES512":
		s := sha512.Sum512([]byte(input))
		digest = s[:]
	default:
		s := sha256.Sum256([]byte(input))
		digest = s[:]
	}
	r, s, err := ecdsa.Sign(rand.Reader, key, digest)
	if err != nil {
		t.Fatal(err)
	}
	sz := (key.Curve.Params().BitSize + 7) / 8
	sig := make([]byte, sz*2)
	r.FillBytes(sig[:sz])
	s.FillBytes(sig[sz:])
	return input + "." + b64(sig)
}

func signRS(t *testing.T, key *rsa.PrivateKey, alg, kid string, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]any{"alg": alg, "typ": "JWT", "kid": kid})
	c, _ := json.Marshal(claims)
	input := b64(h) + "." + b64(c)
	var hash crypto.Hash
	var digest []byte
	switch alg {
	case "RS384", "PS384":
		hash = crypto.SHA384
		s := sha512.Sum384([]byte(input))
		digest = s[:]
	case "RS512", "PS512":
		hash = crypto.SHA512
		s := sha512.Sum512([]byte(input))
		digest = s[:]
	default:
		hash = crypto.SHA256
		s := sha256.Sum256([]byte(input))
		digest = s[:]
	}
	var sig []byte
	var err error
	if alg[0] == 'P' {
		sig, err = rsa.SignPSS(rand.Reader, key, hash, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	} else {
		sig, err = rsa.SignPKCS1v15(rand.Reader, key, hash, digest)
	}
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + b64(sig)
}

func serveJWKS(t *testing.T, keys []map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jwkEC(kid, crv, alg string, pub *ecdsa.PublicKey) map[string]any {
	m := map[string]any{"kty": "EC", "kid": kid, "crv": crv,
		"x": b64(pub.X.Bytes()), "y": b64(pub.Y.Bytes())}
	if alg != "" {
		m["alg"] = alg
	}
	return m
}

// TestECAlgMatrix ES256/384/512 与对应曲线。
func TestECAlgMatrix(t *testing.T) {
	cases := []struct {
		alg   string
		curve elliptic.Curve
		crv   string
	}{
		{"ES256", elliptic.P256(), "P-256"},
		{"ES384", elliptic.P384(), "P-384"},
		{"ES512", elliptic.P521(), "P-521"},
	}
	for _, c := range cases {
		key, err := ecdsa.GenerateKey(c.curve, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		srv := serveJWKS(t, []map[string]any{jwkEC("ec-"+c.alg, c.crv, c.alg, &key.PublicKey)})
		ks := NewKeySet(srv.Client(), srv.URL)
		tok := signEC(t, key, c.alg, "ec-"+c.alg, baseClaims("n"))
		if _, err := VerifyJWT(context.Background(), tok, ks, "https://issuer.example", "client-1", "n"); err != nil {
			t.Fatalf("%s 校验失败: %v", c.alg, err)
		}
	}
}

// TestECWithoutAlgField JWKS 里没有 alg 字段（只有 kid/crv）也要能用。
func TestECWithoutAlgField(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := serveJWKS(t, []map[string]any{jwkEC("only-kid", "P-256", "", &key.PublicKey)})
	ks := NewKeySet(srv.Client(), srv.URL)
	tok := signEC(t, key, "ES256", "only-kid", baseClaims("n"))
	if _, err := VerifyJWT(context.Background(), tok, ks, "https://issuer.example", "client-1", "n"); err != nil {
		t.Fatalf("无 alg 字段的 EC 校验失败: %v", err)
	}
}

// TestRSAAlgMatrix RS/PS 各档。
func TestRSAAlgMatrix(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	n := b64(key.PublicKey.N.Bytes())
	e := b64(big.NewInt(int64(key.PublicKey.E)).Bytes())
	for _, alg := range []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512"} {
		srv := serveJWKS(t, []map[string]any{{
			"kty": "RSA", "kid": "r-" + alg, "alg": alg, "n": n, "e": e,
		}})
		ks := NewKeySet(srv.Client(), srv.URL)
		tok := signRS(t, key, alg, "r-"+alg, baseClaims("n"))
		if _, err := VerifyJWT(context.Background(), tok, ks, "https://issuer.example", "client-1", "n"); err != nil {
			t.Fatalf("%s 校验失败: %v", alg, err)
		}
	}
}
