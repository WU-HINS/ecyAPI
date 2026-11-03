package remote

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeDAV 一个最小可用的 WebDAV 服务：PROPFIND（Depth 1）+ GET。
type fakeDAV struct {
	files map[string]string // 相对路径 → 内容
	dirs  map[string]bool
}

func newFakeDAV() *fakeDAV {
	return &fakeDAV{files: map[string]string{}, dirs: map[string]bool{"": true}}
}

func (f *fakeDAV) add(path, body string) {
	f.files[path] = body
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		f.dirs[strings.Join(parts[:i], "/")] = true
	}
}

func (f *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel := strings.Trim(strings.TrimPrefix(r.URL.Path, "/dav"), "/")
	switch r.Method {
	case "PROPFIND":
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">`)
		add := func(p string, dir bool, size int, mt string) {
			href := "/dav/" + p
			if dir {
				href += "/"
			}
			b.WriteString("<D:response><D:href>" + href + "</D:href><D:propstat><D:prop>")
			if dir {
				b.WriteString("<D:resourcetype><D:collection/></D:resourcetype>")
			} else {
				b.WriteString("<D:resourcetype/>")
				fmt.Fprintf(&b, "<D:getcontentlength>%d</D:getcontentlength>", size)
				if mt != "" {
					b.WriteString("<D:getlastmodified>" + mt + "</D:getlastmodified>")
				}
			}
			b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>")
		}
		add(rel, true, 0, "")
		prefix := ""
		if rel != "" {
			prefix = rel + "/"
		}
		kids := map[string]bool{}
		for d := range f.dirs {
			if d == rel || d == "" || !strings.HasPrefix(d, prefix) {
				continue
			}
			rest := strings.TrimPrefix(d, prefix)
			if !strings.Contains(rest, "/") && rest != "" {
				kids[rest] = true
			}
		}
		for k := range kids {
			add(prefix+k, true, 0, "")
		}
		for p, body := range f.files {
			if !strings.HasPrefix(p, prefix) {
				continue
			}
			rest := strings.TrimPrefix(p, prefix)
			if strings.Contains(rest, "/") {
				continue
			}
			add(p, false, len(body), "Wed, 01 Jan 2025 00:00:00 GMT")
		}
		b.WriteString("</D:multistatus>")
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, b.String())
	case http.MethodGet:
		body, ok := f.files[rel]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// TestMirrorWebDAV WebDAV 增量镜像：首次全下，第二次不重复下载，内容正确。
func TestMirrorWebDAV(t *testing.T) {
	dav := newFakeDAV()
	dav.add("pc/a.png", "AAA")
	dav.add("pc/b.png", "BBBB")
	dav.add("pixiv/2024/x.png", "CCCCC")
	srv := httptest.NewServer(dav)
	defer srv.Close()

	src, err := NewWebDAV(srv.URL+"/dav", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	opt := MirrorOptions{Dest: dest, Workers: 3, MaxDepth: 3}

	res, err := Mirror(context.Background(), src, opt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 3 {
		t.Fatalf("首次应新增 3 个文件，实际 %+v", res)
	}
	body, err := os.ReadFile(filepath.Join(dest, "pixiv", "2024", "x.png"))
	if err != nil || string(body) != "CCCCC" {
		t.Fatalf("内容不对: %q %v", body, err)
	}

	// 第二次：全部命中，不再下载
	res2, err := Mirror(context.Background(), src, opt)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Added != 0 || res2.Updated != 0 {
		t.Fatalf("第二次不应再下载，实际 %+v", res2)
	}

	// 远端新增一个文件 → 只下这一个
	dav.add("pc/c.png", "DD")
	res3, _ := Mirror(context.Background(), src, opt)
	if res3.Added != 1 || res3.Updated != 0 {
		t.Fatalf("应只新增 1 个，实际 %+v", res3)
	}

	// prune：远端删掉后本地也删
	delete(dav.files, "pc/c.png")
	opt.Prune = true
	res4, _ := Mirror(context.Background(), src, opt)
	if res4.Deleted != 1 {
		t.Fatalf("应删除 1 个，实际 %+v", res4)
	}
}

// fakeS3 最小 S3（忽略签名，校验有 Authorization 即可）：ListObjectsV2 + GetObject。
type fakeS3 struct {
	keys map[string]string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	// path-style: /bucket/<key>
	p := strings.TrimPrefix(r.URL.Path, "/bucket")
	if r.URL.Query().Get("list-type") == "2" {
		prefix := r.URL.Query().Get("prefix")
		type cp struct{ name string }
		prefixes := map[string]bool{}
		keys := make([]string, 0, len(f.keys))
		for k := range f.keys {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			rest := strings.TrimPrefix(k, prefix)
			if i := strings.Index(rest, "/"); i >= 0 {
				prefixes[rest[:i+1]] = true
				continue
			}
			keys = append(keys, rest)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
		for p := range prefixes {
			b.WriteString("<CommonPrefixes><Prefix>" + prefix + p + "</Prefix></CommonPrefixes>")
		}
		for _, k := range keys {
			fmt.Fprintf(&b, "<Contents><Key>%s%s</Key><Size>%d</Size><LastModified>2025-01-01T00:00:00.000Z</LastModified><ETag>`%s`</ETag></Contents>",
				prefix, k, len(f.keys[prefix+k]), "etag-"+k)
		}
		b.WriteString("</ListBucketResult>")
		_, _ = io.WriteString(w, b.String())
		return
	}
	key := strings.TrimPrefix(p, "/")
	if body, ok := f.keys[key]; ok {
		_, _ = io.WriteString(w, body)
		return
	}
	http.NotFound(w, r)
}

// TestMirrorS3 S3 增量镜像：目录聚合 + 下载 + 增量。
func TestMirrorS3(t *testing.T) {
	f := &fakeS3{keys: map[string]string{
		"lib/pc/a.png":         "AAA",
		"lib/pixiv/2024/x.png": "CCC",
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	src, err := NewS3(S3Options{
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "bucket",
		Prefix: "lib", AccessKey: "ak", SecretKey: "sk", PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	res, err := Mirror(context.Background(), src, MirrorOptions{Dest: dest, Workers: 2, MaxDepth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 {
		t.Fatalf("应新增 2 个文件，实际 %+v", res)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "pc", "a.png")); err != nil || string(b) != "AAA" {
		t.Fatalf("内容不对: %q %v", b, err)
	}
	res2, _ := Mirror(context.Background(), src, MirrorOptions{Dest: dest, Workers: 2, MaxDepth: 3})
	if res2.Added != 0 || res2.Updated != 0 {
		t.Fatalf("第二次不应重复下载，实际 %+v", res2)
	}
}

var _ = time.Second
