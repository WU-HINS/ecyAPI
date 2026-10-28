package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestDepthConfigurable 层级上限可配置：默认 3 段，配 5 段才能看到 a/b/c/d/e。
func TestDepthConfigurable(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "categories")
	deep := filepath.Join(dataDir, "a", "b", "c", "d", "e")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "f.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	def, err := NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if cats, _ := def.Categories(context.Background()); len(cats) != 0 {
		t.Fatalf("默认深度 3 不应看到第 5 层: %v", cats)
	}

	deep5, err := NewFileStoreDepth(dataDir, 5)
	if err != nil {
		t.Fatal(err)
	}
	cats, _ := deep5.Categories(context.Background())
	if len(cats) != 1 || cats[0] != "a/b/c/d/e" {
		t.Fatalf("深度 5 应看到 a/b/c/d/e，实际 %v", cats)
	}
	if n, err := deep5.Count(context.Background(), "a/b/c/d/e"); err != nil || n != 1 {
		t.Fatalf("统计 = %d, err = %v", n, err)
	}

	// 超上限按 8 处理
	clamped, err := NewFileStoreDepth(dataDir, 99)
	if err != nil {
		t.Fatal(err)
	}
	if cats, _ := clamped.Categories(context.Background()); len(cats) != 1 {
		t.Fatalf("夹取到 8 后应仍能看到: %v", cats)
	}
}
