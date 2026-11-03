package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
	"ecyapi/internal/index"
	"ecyapi/internal/remote"
)

// TestWebNamespace M4 验收：/@web 302 只在本域；/@web/pic 代理出真字节。
func TestWebNamespace(t *testing.T) {
	dav := newFakeDAV(t)
	dav.add("pc/a.png", "PNGDATA-A")
	dav.add("pc/b.png", "PNGDATA-B")
	srv := httptest.NewServer(dav)
	defer srv.Close()

	src, err := remote.NewWebDAV(srv.URL+"/dav", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(t.TempDir(), "web.bin")
	if _, err := remote.BuildIndexFromSource(context.Background(), src, idxPath,
		remote.IndexOptions{MaxDepth: 3, Extensions: []string{".png"}}); err != nil {
		t.Fatal(err)
	}

	s, err := New(config.Config{
		DataDir: t.TempDir(), AdminPassword: "pw", PreviewMP: 16,
		WebIndex: idxPath, WebKind: "webdav", WebURL: srv.URL + "/dav",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.web == nil {
		t.Fatal("云端后端未就绪")
	}

	// /@web/pc → 302，Location 必须是本域 /@web/pic/...
	rec := get(t, s, "/@web/pc")
	if rec.Code != http.StatusFound {
		t.Fatalf("/@web/pc = %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/@web/pic/pc/") {
		t.Fatalf("302 目标不应指向远端: %s", loc)
	}
	if strings.Contains(loc, "127.0.0.1") && !strings.Contains(loc, "/@web/") {
		t.Fatalf("泄露了后端地址: %s", loc)
	}

	// /@web/pic/pc/a.png → 200 且内容来自远端
	rec2 := get(t, s, "/@web/pic/pc/a.png")
	if rec2.Code != http.StatusOK {
		t.Fatalf("/@web/pic = %d", rec2.Code)
	}
	if rec2.Body.String() != "PNGDATA-A" {
		t.Fatalf("代理内容不对: %q", rec2.Body.String())
	}
	if cc := rec2.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=604800") {
		t.Fatalf("缓存头缺失: %q", cc)
	}

	// 不存在的文件 → 404
	if rec := get(t, s, "/@web/pic/pc/none.png"); rec.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404，得到 %d", rec.Code)
	}
	// 未配置时（另一个实例）应 404 而不是 panic
	s2, _ := New(config.Config{DataDir: t.TempDir(), AdminPassword: "pw", PreviewMP: 16})
	if rec := get(t, s2, "/@web/pc"); rec.Code != http.StatusNotFound {
		t.Fatalf("未配置应 404，得到 %d", rec.Code)
	}
	_ = os.Remove(idxPath)
}

// fakeDAV 复用一个最小 WebDAV 服务（PROPFIND + GET）。
type fakeDAVt struct {
	files map[string]string
	dirs  map[string]bool
}

func newFakeDAV(t *testing.T) *fakeDAVt {
	t.Helper()
	return &fakeDAVt{files: map[string]string{}, dirs: map[string]bool{"": true}}
}

func (f *fakeDAVt) add(path, body string) {
	f.files[path] = body
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		f.dirs[strings.Join(parts[:i], "/")] = true
	}
}

func (f *fakeDAVt) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel := strings.Trim(strings.TrimPrefix(r.URL.Path, "/dav"), "/")
	switch r.Method {
	case "PROPFIND":
		var b strings.Builder
		b.WriteString("<?xml version=\"1.0\"?><D:multistatus xmlns:D=\"DAV:\">")
		emit := func(p string, dir bool, size int) {
			href := "/dav/" + p
			if dir {
				href += "/"
			}
			b.WriteString("<D:response><D:href>" + href + "</D:href><D:propstat><D:prop>")
			if dir {
				b.WriteString("<D:resourcetype><D:collection/></D:resourcetype>")
			} else {
				b.WriteString("<D:resourcetype/>")
			}
			b.WriteString("</D:prop></D:propstat></D:response>")
		}
		emit(rel, true, 0)
		prefix := ""
		if rel != "" {
			prefix = rel + "/"
		}
		for d := range f.dirs {
			if d == "" || d == rel || !strings.HasPrefix(d, prefix) {
				continue
			}
			rest := strings.TrimPrefix(d, prefix)
			if rest != "" && !strings.Contains(rest, "/") {
				emit(prefix+rest, true, 0)
			}
		}
		for p, body := range f.files {
			if !strings.HasPrefix(p, prefix) {
				continue
			}
			rest := strings.TrimPrefix(p, prefix)
			if strings.Contains(rest, "/") {
				continue
			}
			emit(p, false, len(body))
		}
		b.WriteString("</D:multistatus>")
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		_, _ = w.Write([]byte(b.String()))
	case http.MethodGet:
		body, ok := f.files[rel]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}
}

var _ = index.OriLand
var _ = config.Config{}
