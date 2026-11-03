package remote

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Options S3 / MinIO / 兼容网关的连接参数。
type S3Options struct {
	Endpoint  string // 如 https://s3.example.com 或 https://minio.local:9000
	Region    string
	Bucket    string
	Prefix    string // 只同步这个前缀下的对象（相当于子目录）
	AccessKey string
	SecretKey string
	PathStyle bool // MinIO / OpenList 常用 path-style；AWS 用 virtual-host
}

// S3 实现 Source（ListObjectsV2 + GetObject，SigV4 签名）。
type S3 struct {
	opt    S3Options
	client *http.Client
}

// NewS3 构造客户端。
func NewS3(opt S3Options) (*S3, error) {
	if opt.Endpoint == "" || opt.Bucket == "" {
		return nil, fmt.Errorf("S3 需要 endpoint 与 bucket")
	}
	if opt.Region == "" {
		opt.Region = "us-east-1"
	}
	if _, err := url.Parse(opt.Endpoint); err != nil {
		return nil, fmt.Errorf("S3 endpoint 不合法: %w", err)
	}
	return &S3{opt: opt, client: &http.Client{Timeout: 60 * time.Second}}, nil
}

func (s *S3) Describe() string {
	return fmt.Sprintf("s3 %s/%s prefix=%q", s.opt.Endpoint, s.opt.Bucket, s.opt.Prefix)
}

// List 列出 prefix/ 下的对象（一次最多 1000，自动翻页），按 "/" 聚合成一层目录。
func (s *S3) List(ctx context.Context, dir string) ([]Entry, error) {
	prefix := strings.Trim(s.opt.Prefix, "/")
	if dir != "" {
		if prefix != "" {
			prefix += "/"
		}
		prefix += strings.Trim(dir, "/")
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	seen := map[string]bool{}
	out := make([]Entry, 0, 64)
	token := ""
	for {
		q := url.Values{}
		q.Set("list-type", "2")
		q.Set("delimiter", "/")
		q.Set("max-keys", "1000")
		if prefix != "" {
			q.Set("prefix", prefix)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		body, err := s.do(ctx, http.MethodGet, "/", q, nil)
		if err != nil {
			return nil, err
		}
		var page listBucketResult
		if err := xml.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("解析 S3 列表失败: %w", err)
		}
		for _, cp := range page.CommonPrefixes {
			name := strings.TrimSuffix(strings.TrimPrefix(cp.Prefix, prefix), "/")
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, Entry{Path: joinRel(dir, name), IsDir: true})
		}
		for _, obj := range page.Contents {
			rel := strings.TrimPrefix(obj.Key, prefix)
			if rel == "" || strings.HasSuffix(rel, "/") {
				continue // 目录占位对象
			}
			if strings.Contains(rel, "/") {
				continue // delimiter 应该已经聚合掉，保险起见跳过
			}
			p := joinRel(dir, rel)
			if seen[p] {
				continue
			}
			seen[p] = true
			e := Entry{Path: p, Size: obj.Size, ETag: strings.Trim(obj.ETag, `"`)}
			if t, err := time.Parse(time.RFC3339, obj.LastModified); err == nil {
				e.ModTime = t
			}
			out = append(out, e)
		}
		if page.IsTruncated != "true" || page.NextToken == "" {
			break
		}
		token = page.NextToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Open 下载对象。
func (s *S3) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	key := strings.Trim(s.opt.Prefix, "/")
	if key != "" {
		key += "/"
	}
	key += strings.TrimLeft(rel, "/")
	resp, err := s.doRaw(ctx, http.MethodGet, "/"+url.PathEscape(key), nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", key, resp.StatusCode)
	}
	return resp.Body, nil
}

type listBucketResult struct {
	IsTruncated string `xml:"IsTruncated"`
	NextToken   string `xml:"NextContinuationToken"`
	Contents    []struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return strings.Trim(dir, "/") + "/" + name
}

// do 发起请求并读完 body。
func (s *S3) do(ctx context.Context, method, path string, q url.Values, body io.Reader) ([]byte, error) {
	resp, err := s.doRaw(ctx, method, path, q, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("S3 %s %s: HTTP %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return io.ReadAll(resp.Body)
}

// doRaw 带 SigV4 的请求。
func (s *S3) doRaw(ctx context.Context, method, path string, q url.Values, body io.Reader) (*http.Response, error) {
	u, err := url.Parse(s.opt.Endpoint)
	if err != nil {
		return nil, err
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := u.Host
	basePath := strings.TrimRight(u.Path, "/")
	if s.opt.PathStyle {
		path = basePath + "/" + s.opt.Bucket + path
	} else {
		host = s.opt.Bucket + "." + host
		path = basePath + path
	}
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	req, err := http.NewRequestWithContext(ctx, method, scheme+"://"+host+path, body)
	if err != nil {
		return nil, err
	}
	if q != nil {
		req.URL.RawQuery = q.Encode()
	}
	req.Host = host

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := emptySHA256
	if body != nil {
		return nil, fmt.Errorf("暂不支持带请求体的 S3 调用")
	}
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	// 规范化请求
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonicalQuery := ""
	if q != nil {
		canonicalQuery = canonicalQueryString(q)
	}
	canonicalRequest := strings.Join([]string{
		method, path, canonicalQuery, canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, s.opt.Region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	k := hmacSHA256([]byte("AWS4"+s.opt.SecretKey), dateStamp)
	k = hmacSHA256(k, s.opt.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.opt.AccessKey, scope, signedHeaders, sig))
	return s.client.Do(req)
}

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// canonicalQueryString 按 S3 规范排序并编码查询参数。
func canonicalQueryString(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(awsEscape(k))
		b.WriteByte('=')
		b.WriteString(awsEscape(q.Get(k)))
	}
	return b.String()
}

func awsEscape(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "%7E", "~")
	return e
}
