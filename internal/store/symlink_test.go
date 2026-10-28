package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSymlinkDirIsCategory 指向目录的符号链接应被视为分类（常见：ln -s 外部图库）。
func TestSymlinkDirIsCategory(t *testing.T) {
	base := t.TempDir()
	ext := filepath.Join(base, "extlib")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, "a.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(base, "categories")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ext, filepath.Join(dataDir, "linked")); err != nil {
		t.Skipf("当前环境不支持符号链接: %v", err)
	}
	st, err := NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	cats, _ := st.Categories(context.Background())
	if len(cats) != 1 || cats[0] != "linked" {
		t.Fatalf("分类列表 = %v，期望 [linked]", cats)
	}
	rows, err := st.Random(context.Background(), "linked", 1)
	if err != nil || len(rows) != 1 || rows[0].Link != "/pic/linked/a.png" {
		t.Fatalf("随机取图 = %+v, err = %v", rows, err)
	}
}

// TestSymlinkLoopSkipped 回环符号链接不能导致无限递归，且要记入诊断。
func TestSymlinkLoopSkipped(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "categories")
	if err := os.MkdirAll(filepath.Join(dataDir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "real", "a.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dataDir, filepath.Join(dataDir, "loop")); err != nil {
		t.Skipf("当前环境不支持符号链接: %v", err)
	}
	st, err := NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	cats, _ := st.Categories(context.Background())
	if len(cats) != 1 || cats[0] != "real" {
		t.Fatalf("分类列表 = %v，期望只有 real", cats)
	}
	warns := ScanWarnings(st)
	if len(warns) == 0 {
		t.Fatalf("回环链接应产生诊断信息")
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "回环") && !strings.Contains(joined, "重复") {
		t.Fatalf("诊断内容未说明回环: %v", warns)
	}
}

// TestScanWarningsExplainSkipped 非法目录名/指向文件的链接要给出原因。
func TestScanWarningsExplainSkipped(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "categories")
	if err := os.MkdirAll(filepath.Join(dataDir, "ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "ok", "a.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 非法名字：横线 / 数字开头
	for _, name := range []string{"2024-01", "1abc"} {
		if err := os.MkdirAll(filepath.Join(dataDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 指向文件的链接
	if err := os.WriteFile(filepath.Join(base, "f"), []byte("x"), 0o644); err == nil {
		_ = os.Symlink(filepath.Join(base, "f"), filepath.Join(dataDir, "filelink"))
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
	if !strings.Contains(joined, "2024-01") || !strings.Contains(joined, "不合法") {
		t.Fatalf("缺少非法名字诊断: %s", joined)
	}
	if strings.Contains(joined, "filelink") && !strings.Contains(joined, "不是目录") {
		t.Fatalf("指向文件的链接诊断不明确: %s", joined)
	}
}
