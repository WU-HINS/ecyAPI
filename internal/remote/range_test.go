package remote

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOpenRange 验证「只取文件头」：206 与忽略 Range 的 200 都要正确截断。
func TestOpenRange(t *testing.T) {
	body := "HEADER0123456789ABCDEF" // 24 字节
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-7" {
			w.Header().Set("Content-Range", "bytes 0-7/24")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, body[:8])
			return
		}
		// 忽略 Range，直接 200 全量
		_, _ = io.WriteString(w, body)
	}))
	defer dav.Close()

	src, _ := NewWebDAV(dav.URL, "", "", "")

	rc, err := src.OpenRange(context.Background(), "x.jpg", 8)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != body[:8] {
		t.Fatalf("206 分支应只取 8 字节，得到 %q", got)
	}

	// 远端忽略 Range：也必须只返回 8 字节（不能把整张图拉下来）
	rc2, err := src.OpenRange(context.Background(), "y.jpg", 8)
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := io.ReadAll(rc2)
	rc2.Close()
	if len(got2) != 8 {
		t.Fatalf("200 分支应截断到 8 字节，得到 %d 字节", len(got2))
	}
}
