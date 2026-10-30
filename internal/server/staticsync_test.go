package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ecyapi/internal/config"
)

// newStaticServerWith 用指定目录构造实例（测试静态资源回落用）。
func newStaticServerWith(t *testing.T, dataDir, staticDir, syncFrom string) *Server {
	t.Helper()
	s, err := New(config.Config{
		DataDir: dataDir, AdminPassword: "pw", PreviewMP: 16,
		StaticDir: staticDir, StaticSyncFrom: syncFrom,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSyncTree 只补缺失/更新的文件，已最新的一次都不拷贝。
func TestSyncTree(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	write := func(base, rel, body string, mt time.Time) {
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if !mt.IsZero() {
			_ = os.Chtimes(p, mt, mt)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	now := time.Now()

	write(src, "a.webm", "AAA", old)
	write(src, "sub/b.webm", "BBBB", now)
	write(dst, "a.webm", "AAA", now) // 已存在且更新

	added, updated, err := syncTree(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 || updated != 0 {
		t.Fatalf("首次同步应新增 1（sub/b.webm），实际 added=%d updated=%d", added, updated)
	}
	if _, err := os.Stat(filepath.Join(dst, "sub", "b.webm")); err != nil {
		t.Fatalf("文件未同步: %v", err)
	}

	// 再跑一次：没有变化，不应再拷贝
	added, updated, err = syncTree(src, dst)
	if err != nil || added != 0 || updated != 0 {
		t.Fatalf("幂等失败: added=%d updated=%d err=%v", added, updated, err)
	}

	// 源文件更新（更晚的 mtime）→ 应覆盖
	later := time.Now().Add(time.Minute)
	write(src, "a.webm", "AAAA-updated", later)
	added, updated, err = syncTree(src, dst)
	if err != nil || added != 0 || updated != 1 {
		t.Fatalf("更新失败: added=%d updated=%d err=%v", added, updated, err)
	}
	body, _ := os.ReadFile(filepath.Join(dst, "a.webm"))
	if string(body) != "AAAA-updated" {
		t.Fatalf("内容未更新: %q", body)
	}
}

// TestStaticRootsFallback 运行目录没有文件时，回落到镜像层目录。
func TestStaticRootsFallback(t *testing.T) {
	dataDir := t.TempDir()  // 相当于数据卷（空）
	imageDir := t.TempDir() // 相当于镜像层 /app/static
	if err := os.MkdirAll(filepath.Join(imageDir, "assets", "webm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, "assets", "webm", "x.webm"), []byte("WEBM"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	s := newStaticServerWith(t, filepath.Join(dir, "categories"), dataDir, imageDir)
	rec := get(t, s, "/@index/assets/webm/x.webm")
	if rec.Code != 200 || rec.Body.String() != "WEBM" {
		t.Fatalf("应从镜像层回落读出: %d %q", rec.Code, rec.Body.String())
	}
}
