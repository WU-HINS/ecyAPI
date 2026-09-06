package server

import (
	"os"
	"path/filepath"
	"testing"

	"ecyapi/internal/config"
)

// 端到端：模拟线上事故现场——endpoints.json 是「单个对象」的旧格式，
// 修复前 New() 会返回 "parse data/endpoints.json: cannot unmarshal object ..."。
func TestLoadEndpointsSingleObjectFile(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// endpointsFile 常量是相对路径 "data/endpoints.json"，测试进程切到临时目录。
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	bad := `{"path":"cat","desk":"cat","mobile":"","label":"CAT","desc":"","kind":"image"}`
	if err := os.WriteFile("data/endpoints.json", []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{DataDir: dataDir}
	_, err := New(cfg)
	if err != nil {
		t.Fatalf("New should tolerate single-object endpoints.json, got: %v", err)
	}
}
