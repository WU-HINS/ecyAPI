package remote

// webdav_real_test.go —— 模拟「OpenList + 网盘」在真实世界里会出现的几种行为，
// 验证我们的 WebDAV 客户端能不能扛住：
//   1) WebDAV 不代理字节，而是 302 到云存储/CDN（最常见）
//   2) 返回 206 Partial（部分存储按 Range 服务）
//   3) 403 / 401（鉴权失败或链接过期）
//   4) chunked 慢速流（大文件被网盘限速）

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cdn 模拟「云存储直链」：不校验任何鉴权头，直接吐字节。
func cdnServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			// 真实 CDN 会拒绝带 Origin 凭据的请求；我们的客户端跨域重定向时
			// 由 Go 自动剥掉 Authorization，这里顺带验证这一点。
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "unexpected auth")
			return
		}
		_, _ = io.WriteString(w, body)
	}))
}

// TestWebDAVRedirectToCDN WebDAV 层 302 到 CDN 时能拿到字节。
func TestWebDAVRedirectToCDN(t *testing.T) {
	cdn := cdnServer(t, "CDN-BYTES")
	defer cdn.Close()

	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cdn.URL+"/real.jpg", http.StatusFound)
	}))
	defer dav.Close()

	src, err := NewWebDAV(dav.URL, "user", "pass", "")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := src.Open(context.Background(), "a/b.jpg")
	if err != nil {
		t.Fatalf("跟随重定向失败: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "CDN-BYTES" {
		t.Fatalf("拿到的内容不对: %q", got)
	}
}

// TestWebDAVPartial 存储返回 206 时也能正常读取。
func TestWebDAVPartial(t *testing.T) {
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-4/100")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "HELLO")
	}))
	defer dav.Close()

	src, _ := NewWebDAV(dav.URL, "", "", "")
	rc, err := src.Open(context.Background(), "x.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "HELLO" {
		t.Fatalf("206 内容不对: %q", got)
	}
}

// TestWebDAVAuthRejected 403 必须变成错误（不能静默返回空）。
func TestWebDAVAuthRejected(t *testing.T) {
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer dav.Close()

	src, _ := NewWebDAV(dav.URL, "u", "wrong", "")
	if _, err := src.Open(context.Background(), "x.jpg"); err == nil {
		t.Fatal("403 应当返回错误")
	}
}

// TestWebDAVSlowChunked 网盘限速（分块慢速）时，我们的「读头 + 续传」模式不卡死。
func TestWebDAVSlowChunked(t *testing.T) {
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		chunks := []string{"AAAA", "BBBB", "CCCC"}
		for _, c := range chunks {
			_, _ = io.WriteString(w, c)
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer dav.Close()

	src, _ := NewWebDAV(dav.URL, "", "", "")
	rc, err := src.Open(context.Background(), "big.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	// 模拟服务端「先读头判方向、再续传」的读取方式
	head := make([]byte, 8)
	n, _ := io.ReadFull(rc, head)
	rest, _ := io.ReadAll(rc)
	got := string(head[:n]) + string(rest)
	if got != "AAAABBBBCCCC" {
		t.Fatalf("分块读取内容不对: %q", got)
	}
	if !strings.HasPrefix(got, "AAAA") {
		t.Fatalf("头部顺序不对: %q", got)
	}
}
