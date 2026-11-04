package remote

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// WebDAV 实现 Source，兼容 OpenList / AList / Nextcloud / 群晖等标准 WebDAV 服务。
type WebDAV struct {
	Base     string // 例如 https://openlist.example.com/dav
	User     string
	Pass     string
	Token    string // 可选：Authorization: Bearer xxx（OpenList 也支持）
	Client   *http.Client
	rootPath string // 从 Base 解析出的路径部分，用来把 PROPFIND 的 href 还原成相对路径
}

// NewWebDAV 构造客户端；base 需指向 WebDAV 根（OpenList 通常是 /dav）。
func NewWebDAV(base, user, pass, token string) (*WebDAV, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("WebDAV 地址不合法: %w", err)
	}
	return &WebDAV{
		Base:  strings.TrimRight(base, "/"),
		User:  user,
		Pass:  pass,
		Token: token,
		Client: &http.Client{
			Timeout: 60 * time.Second,
			// 不自动跟随：跨主机重定向时 Go 会把请求头带过去（含 Authorization），
			// 很多云存储/CDN 见到 Origin 凭据会直接 400/403。改成自己跟随、跨域时去掉凭据。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		rootPath: strings.TrimRight(u.Path, "/"),
	}, nil
}

func (w *WebDAV) Describe() string { return "webdav " + w.Base }

func (w *WebDAV) newRequest(ctx context.Context, method, rel string) (*http.Request, error) {
	if strings.Contains(rel, "..") {
		return nil, fmt.Errorf("非法路径: %s", rel)
	}
	u := w.Base + "/" + strings.TrimLeft(rel, "/")
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	if w.Token != "" {
		req.Header.Set("Authorization", "Bearer "+w.Token)
	} else if w.User != "" {
		req.SetBasicAuth(w.User, w.Pass)
	}
	return req, nil
}

type davResponse struct {
	XMLName  xml.Name  `xml:"response"`
	Href     string    `xml:"href"`
	Propstat []davProp `xml:"propstat"`
}

type davProp struct {
	Prop davPropInner `xml:"prop"`
}

type davPropInner struct {
	ContentLength int64  `xml:"getcontentlength"`
	LastModified  string `xml:"getlastmodified"`
	ResourceType  struct {
		Collection *struct{} `xml:"collection"`
	} `xml:"resourcetype"`
	ETag string `xml:"getetag"`
}

type davMultistatus struct {
	XMLName   xml.Name      `xml:"multistatus"`
	Responses []davResponse `xml:"response"`
}

// List 用 PROPFIND（Depth: 1）列出一层。
func (w *WebDAV) List(ctx context.Context, dir string) ([]Entry, error) {
	req, err := w.newRequest(ctx, "PROPFIND", dir)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml")
	body := `<?xml version="1.0"?><propfind xmlns="DAV:"><prop><resourcetype/><getcontentlength/><getlastmodified/><getetag/></prop></propfind>`
	req.Body = io.NopCloser(strings.NewReader(body))
	req.ContentLength = int64(len(body))

	resp, err := w.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("PROPFIND %s: %w", dir, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("PROPFIND %s: HTTP %d %s", dir, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var ms davMultistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("解析 PROPFIND 响应失败: %w", err)
	}

	base := strings.TrimRight(dir, "/")
	out := make([]Entry, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		rel, ok := w.relPath(r.Href)
		if !ok || rel == "" || rel == base {
			continue // 跳过目录自身与根
		}
		var prop davPropInner
		if len(r.Propstat) > 0 {
			prop = r.Propstat[0].Prop
		}
		e := Entry{
			Path:  rel,
			IsDir: prop.ResourceType.Collection != nil || strings.HasSuffix(r.Href, "/"),
			Size:  prop.ContentLength,
			ETag:  strings.Trim(prop.ETag, `"`),
		}
		if prop.LastModified != "" {
			if t, err := http.ParseTime(prop.LastModified); err == nil {
				e.ModTime = t
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// relPath 把 PROPFIND 的 href 还原成相对根的路径。
func (w *WebDAV) relPath(href string) (string, bool) {
	u, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	p := u.Path
	if !strings.HasPrefix(p, w.rootPath) {
		return "", false
	}
	p = strings.TrimPrefix(p, w.rootPath)
	// 归一：去掉多余的 /、./，并统一成「无前导斜杠」的相对路径
	p = strings.Trim(path.Clean("/"+p), "/")
	return p, true
}

// Open 下载一个文件。
//
// 与 OpenList/网盘的实战要点：
//   - 很多存储不直接吐字节，而是 302 到云存储直链；跨主机时必须**去掉 Authorization**，
//     否则 CDN 会返回 400/403（浏览器行为也是如此）。
//   - 部分存储返回 206 Partial，同样应当当成正常流。
func (w *WebDAV) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	req, err := w.newRequest(ctx, http.MethodGet, rel)
	if err != nil {
		return nil, err
	}
	resp, err := w.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", rel, err)
	}
	// 手动跟随重定向（最多 3 跳）
	for hop := 0; hop < 3 && isRedirect(resp.StatusCode); hop++ {
		loc := resp.Header.Get("Location")
		_ = resp.Body.Close()
		if loc == "" {
			return nil, fmt.Errorf("GET %s: 重定向缺少 Location", rel)
		}
		next, err := req.URL.Parse(loc)
		if err != nil {
			return nil, fmt.Errorf("GET %s: 重定向地址非法: %w", rel, err)
		}
		req2, err := http.NewRequestWithContext(ctx, http.MethodGet, next.String(), nil)
		if err != nil {
			return nil, err
		}
		// 只有同主机才带凭据；跨主机（云存储直链）一律裸请求
		if next.Host == req.URL.Host {
			for k, v := range req.Header {
				req2.Header[k] = v
			}
		}
		resp, err = w.Client.Do(req2)
		if err != nil {
			return nil, fmt.Errorf("GET %s（重定向后）: %w", rel, err)
		}
		req = req2
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", rel, resp.StatusCode)
	}
	return resp.Body, nil
}

func isRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}
