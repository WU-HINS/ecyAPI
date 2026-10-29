// Package oidc 提供 OAuth 2.0 / OpenID Connect 登录所需的最小实现：
// 发现文档、授权码交换、JWKS 校验（RS/PS/ES 全系）、userinfo。
//
// 刻意不引入第三方依赖：只用标准库的 crypto/rsa、crypto/ecdsa 与 encoding/json。
package oidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// ErrInvalidToken 表示 JWT 校验失败。
var ErrInvalidToken = errors.New("oidc: invalid token")

// Claims 关心的标准声明（其余放在 Raw 里）。
type Claims struct {
	Iss               string
	Sub               string
	Aud               []string
	Exp               int64
	Iat               int64
	Nonce             string
	Email             string
	Name              string
	PreferredUsername string
	Raw               map[string]any
}

// Key 是 JWKS 中的一把公钥。
type Key struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// JWKS 是 /jwks 文档。
type JWKS struct {
	Keys []Key `json:"keys"`
}

// PublicKey 把 JWK 还原成 crypto.PublicKey（RSA 或 ECDSA）。
func (k Key) PublicKey() (crypto.PublicKey, error) {
	switch strings.ToUpper(k.Kty) {
	case "RSA":
		n, err := b64ToBigInt(k.N)
		if err != nil {
			return nil, err
		}
		e, err := b64ToBigInt(k.E)
		if err != nil {
			return nil, err
		}
		if !e.IsInt64() || e.Int64() <= 1 {
			return nil, fmt.Errorf("oidc: bad RSA exponent")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch strings.ToUpper(k.Crv) {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("oidc: unsupported curve %q", k.Crv)
		}
		x, err := b64ToBigInt(k.X)
		if err != nil {
			return nil, err
		}
		y, err := b64ToBigInt(k.Y)
		if err != nil {
			return nil, err
		}
		if !curve.IsOnCurve(x, y) {
			return nil, fmt.Errorf("oidc: point not on curve")
		}
		return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
	}
	return nil, fmt.Errorf("oidc: unsupported kty %q", k.Kty)
}

func b64ToBigInt(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("oidc: empty b64 value")
	}
	return new(big.Int).SetBytes(b), nil
}

// VerifySignature 用给定公钥校验签名，支持 RS/PS/ES 全系。
func VerifySignature(alg string, key crypto.PublicKey, signingInput, sig []byte) error {
	switch strings.ToUpper(alg) {
	case "RS256", "RS384", "RS512":
		pk, ok := key.(*rsa.PublicKey)
		if !ok {
			return ErrInvalidToken
		}
		h := hashFor(alg)
		// rsa.VerifyPKCS1v15 收的是「摘要」而不是原文，先算哈希。
		if err := rsa.VerifyPKCS1v15(pk, h, digestOf(h, signingInput), sig); err != nil {
			return ErrInvalidToken
		}
		return nil
	case "PS256", "PS384", "PS512":
		pk, ok := key.(*rsa.PublicKey)
		if !ok {
			return ErrInvalidToken
		}
		h := hashFor(alg)
		if err := rsa.VerifyPSS(pk, h, digestOf(h, signingInput), sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}); err != nil {
			return ErrInvalidToken
		}
		return nil
	case "ES256", "ES384", "ES512":
		pk, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return ErrInvalidToken
		}
		var h crypto.Hash
		switch strings.ToUpper(alg) {
		case "ES256":
			h = crypto.SHA256
		case "ES384":
			h = crypto.SHA384
		default:
			h = crypto.SHA512
		}
		if len(sig)%2 != 0 {
			return ErrInvalidToken
		}
		half := len(sig) / 2
		r := new(big.Int).SetBytes(sig[:half])
		s := new(big.Int).SetBytes(sig[half:])
		digest := digestOf(h, signingInput)
		if !ecdsa.Verify(pk, digest, r, s) {
			return ErrInvalidToken
		}
		return nil
	case "none", "":
		return ErrInvalidToken // 明确拒绝无签名
	}
	return fmt.Errorf("oidc: unsupported alg %q", alg)
}

func hashFor(alg string) crypto.Hash {
	switch strings.ToUpper(alg) {
	case "RS384", "PS384":
		return crypto.SHA384
	case "RS512", "PS512":
		return crypto.SHA512
	}
	return crypto.SHA256
}

func digestOf(h crypto.Hash, b []byte) []byte {
	switch h {
	case crypto.SHA384:
		s := sha512.Sum384(b)
		return s[:]
	case crypto.SHA512:
		s := sha512.Sum512(b)
		return s[:]
	}
	s := sha256.Sum256(b)
	return s[:]
}

// ParseUnverified 只解析 payload，不做校验（用于 alg/kid 选择与排障）。
func ParseUnverified(token string) (header map[string]any, claims map[string]any, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, nil, ErrInvalidToken
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, ErrInvalidToken
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, ErrInvalidToken
	}
	if err := json.Unmarshal(hb, &header); err != nil {
		return nil, nil, ErrInvalidToken
	}
	if err := json.Unmarshal(cb, &claims); err != nil {
		return nil, nil, ErrInvalidToken
	}
	return header, claims, nil
}

// KeyID 从 token 头部取 kid / alg。
func KeyID(token string) (kid, alg string, err error) {
	h, _, err := ParseUnverified(token)
	if err != nil {
		return "", "", err
	}
	kid, _ = h["kid"].(string)
	alg, _ = h["alg"].(string)
	return kid, alg, nil
}

// SigningInput 返回待签名串。
func SigningInput(token string) []byte {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return nil
	}
	return []byte(parts[0] + "." + parts[1])
}

// Signature 返回原始签名字节。
func Signature(token string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}
	return base64.RawURLEncoding.DecodeString(parts[2])
}

// CheckClaims 校验 iss/aud/exp/nonce。
func CheckClaims(c Claims, issuer, clientID, nonce string, now time.Time) error {
	if issuer != "" && c.Iss != issuer {
		return fmt.Errorf("%w: iss mismatch", ErrInvalidToken)
	}
	if clientID != "" {
		found := false
		for _, a := range c.Aud {
			if subtle.ConstantTimeCompare([]byte(a), []byte(clientID)) == 1 {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: aud mismatch", ErrInvalidToken)
		}
	}
	if c.Exp > 0 && now.After(time.Unix(c.Exp, 0).Add(60*time.Second)) {
		return fmt.Errorf("%w: expired", ErrInvalidToken)
	}
	if nonce != "" && c.Nonce != nonce {
		return fmt.Errorf("%w: nonce mismatch", ErrInvalidToken)
	}
	return nil
}
