package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRelaxedDirNames 真实目录名里的横线、点、数字开头、中文、空格都应被扫描到。
func TestRelaxedDirNames(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "categories")
	names := []string{"2024-01", "pixiv.1", "1abc", "KuroiDa", "中文目录", "A B"}
	for _, n := range names {
		d := filepath.Join(dataDir, n)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "a.png"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	cats, _ := st.Categories(context.Background())
	if len(cats) != len(names) {
		t.Fatalf("分类数 = %d (%v)，期望 %d", len(cats), cats, len(names))
	}
	for _, n := range names {
		if _, err := st.Count(context.Background(), n); err != nil {
			t.Fatalf("分类 %q 未载入: %v", n, err)
		}
	}
}

// TestReservedDirSkipped 与内置路由同名的目录不载入，并给出原因。
func TestReservedDirSkipped(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "categories")
	for _, n := range []string{"pic", "json", "ok"} {
		d := filepath.Join(dataDir, n)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "a.png"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	cats, _ := st.Categories(context.Background())
	if len(cats) != 1 || cats[0] != "ok" {
		t.Fatalf("分类列表 = %v，期望只有 ok", cats)
	}
	joined := strings.Join(ScanWarnings(st), "\n")
	if !strings.Contains(joined, "pic") || !strings.Contains(joined, "保留名") {
		t.Fatalf("缺少保留名诊断: %s", joined)
	}
}
