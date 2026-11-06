package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestMultiRoot 主库与附加库的分类各自独立，且附加库带命名空间前缀。
func TestMultiRoot(t *testing.T) {
	mainDir := filepath.Join(t.TempDir(), "main")
	extraDir := filepath.Join(t.TempDir(), "extra")
	// 两库都有同名分类「风景」
	writePNG(t, filepath.Join(mainDir, "风景", "a.png"), 20, 10)
	writePNG(t, filepath.Join(extraDir, "风景", "b.png"), 10, 20)

	st, err := NewFileStoreWithOptions(mainDir, Options{
		StartupWait: 10 * time.Second,
		ExtraRoots:  []Root{{Name: "网盘", Path: extraDir}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.(*fileStore).Close()

	names, err := st.Categories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("分类: %q", names)

	// 主库分类名不变
	n, _ := st.CountWith(context.Background(), "风景", OrientationAny)
	if n != 1 {
		t.Fatalf("主库 风景 应有 1 张，实际 %d", n)
	}
	// 附加库分类带前缀
	pref := nsSep + "网盘/风景"
	n2, _ := st.CountWith(context.Background(), pref, OrientationAny)
	if n2 != 1 {
		t.Fatalf("附加库应有 1 张，实际 %d（分类名 %q）", n2, pref)
	}

	// 物理路径解析
	fs := st.(*fileStore)
	if got := fs.physPath("风景"); got != filepath.Join(mainDir, "风景") {
		t.Fatalf("主库路径解析错误: %s", got)
	}
	if got := fs.physPath(pref); got != filepath.Join(extraDir, "风景") {
		t.Fatalf("附加库路径解析错误: %s", got)
	}
	// 对外剥离前缀
	lib, rel := StripNamespace(pref)
	if lib != "网盘" || rel != "风景" {
		t.Fatalf("StripNamespace 错误: %q %q", lib, rel)
	}
}
