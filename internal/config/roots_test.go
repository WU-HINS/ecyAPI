package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRootsFileRoundtrip roots.json 读写与优先级。
func TestRootsFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roots.json")

	if got := LoadRootsFile(path); got != nil {
		t.Fatalf("文件不存在应返回 nil，得到 %v", got)
	}

	in := []ExtraRoot{{Name: "网盘", Path: "/mnt/a"}, {Name: "固态", Path: "/srv/b"}}
	if err := SaveRootsFile(path, in); err != nil {
		t.Fatal(err)
	}
	out := LoadRootsFile(path)
	if len(out) != 2 || out[0].Name != "网盘" || out[1].Path != "/srv/b" {
		t.Fatalf("往返结果不对: %v", out)
	}

	// 损坏的文件不应让启动失败
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadRootsFile(path); got != nil {
		t.Fatalf("损坏文件应返回 nil，得到 %v", got)
	}
}
