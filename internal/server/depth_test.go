package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ecyapi/internal/config"
)

// TestServerHonoursMaxDepth ECY_MAX_DEPTH 决定 URL 能到第几层。
func TestServerHonoursMaxDepth(t *testing.T) {
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
	deep := filepath.Join(dataDir, "x", "y", "z")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "f.png"), subdirPNG(t, 64, 36), 0o644); err != nil {
		t.Fatal(err)
	}

	get := func(maxDepth int, path string) int {
		s, err := New(config.Config{DataDir: dataDir, AdminPassword: "pw", MaxDepth: maxDepth})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Code
	}

	if code := get(2, "/x/y/z"); code != http.StatusNotFound {
		t.Fatalf("MaxDepth=2 时 /x/y/z 应 404，得到 %d", code)
	}
	if code := get(3, "/x/y/z"); code != http.StatusFound {
		t.Fatalf("MaxDepth=3 时 /x/y/z 应 302，得到 %d", code)
	}
	if code := get(5, "/x/y/z"); code != http.StatusFound {
		t.Fatalf("MaxDepth=5 时 /x/y/z 应 302，得到 %d", code)
	}
}
