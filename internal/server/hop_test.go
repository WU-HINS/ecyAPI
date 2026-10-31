package server

// 一跳语义：任意入口（分类 / 父聚合 / 全分类 / 虚拟分组）直接访问时，
// 必须一次 302 就跳到最终图片地址，Location 自己再请求应当是 200（不再 302）。

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
)

func newHopServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "categories")
	for _, p := range []string{"pc/a.png", "pc/b.png", "pixiv/kuro/x.png", "pixiv/kuro/y.png"} {
		full := filepath.Join(dataDir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, subdirPNG(t, 40, 20), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile("data/groups.json",
		[]byte("{\"groups\":[{\"name\":\"mix\",\"categories\":[\"pc\",\"pixiv\"]}]}"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dataDir, AdminPassword: "pw", PreviewMP: 16})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSingleHopRedirect(t *testing.T) {
	s := newHopServer(t)
	entries := []string{"/pc", "/pixiv", "/@img", "/@img/mix"}
	for _, entry := range entries {
		for _, q := range []string{"", "?w=480"} {
			target := entry + q
			rec := get(t, s, target)
			if rec.Code != http.StatusFound {
				t.Fatalf("%s 应 302，得到 %d", target, rec.Code)
			}
			loc := rec.Header().Get("Location")
			if !strings.HasPrefix(loc, "/pic/") {
				t.Fatalf("%s 的 Location 不是最终图片地址: %s", target, loc)
			}
			// 最终地址必须直接 200，不能再跳
			rec2 := get(t, s, loc)
			if rec2.Code != http.StatusOK {
				t.Fatalf("%s → %s 应为 200（一跳到底），得到 %d", target, loc, rec2.Code)
			}
		}
	}
}

// 首页卡片仍用入口（这样每次加载/点击都会重新随机），而不是写死一张图。
func TestHomeCardsUseEndpoint(t *testing.T) {
	s := newHopServer(t)
	rec := get(t, s, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("首页应 200，得到 %d", rec.Code)
	}
	body := rec.Body.String()
	// 卡片预览走入口（一跳 302），不是写死的 /pic/ 地址；
	// 这样每次加载 / 点击都会重新随机。
	if !strings.Contains(body, `data-src="/@img/mix?w=480"`) {
		t.Fatal("首页分组卡片预览应指向 /@img/mix?w=480 入口")
	}

}

// TestRedirectRandomNoStore 随机跳转必须禁止缓存，否则 CDN 会把随机性"冻住"。
func TestRedirectRandomNoStore(t *testing.T) {
	s := newHopServer(t)
	for _, entry := range []string{"/pc", "/pixiv", "/@img", "/@img/mix"} {
		rec := get(t, s, entry)
		if rec.Code != http.StatusFound {
			t.Fatalf("%s 应 302，得到 %d", entry, rec.Code)
		}
		cc := rec.Header().Get("Cache-Control")
		if !strings.Contains(cc, "no-store") {
			t.Fatalf("%s 的 302 未禁止缓存：Cache-Control=%q", entry, cc)
		}
	}
}

// TestRedirectTargetIsCacheable 302 不缓存，但 302 指向的图片必须可缓存（带 ETag）。
func TestRedirectTargetIsCacheable(t *testing.T) {
	s := newHopServer(t)
	rec := get(t, s, "/pc")
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("302 必须 no-store，实际 %q", cc)
	}
	loc := rec.Header().Get("Location")
	if loc == "" {
		t.Fatal("缺少 Location")
	}

	// 原图
	rec2 := get(t, s, loc)
	cc := rec2.Header().Get("Cache-Control")
	if !strings.Contains(cc, "public") || !strings.Contains(cc, "max-age=") {
		t.Fatalf("目标图应可缓存，实际 Cache-Control=%q", cc)
	}
	if rec2.Header().Get("ETag") == "" {
		t.Fatal("目标图缺少 ETag（无法协商缓存）")
	}
	if rec2.Code != http.StatusOK {
		t.Fatalf("目标图应 200，得到 %d", rec2.Code)
	}

	// 缩略图目标同样可缓存
	rec3 := get(t, s, loc+"?w=480")
	if cc := rec3.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=") {
		t.Fatalf("缩略图应可缓存，实际 %q", cc)
	}
	if rec3.Code != http.StatusOK {
		t.Fatalf("缩略图应 200，得到 %d", rec3.Code)
	}
}
