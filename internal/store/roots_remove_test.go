package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestRemoveExtraRoot 删除附加库后，其分类消失（URL 随之 404）。
func TestRemoveExtraRoot(t *testing.T) {
	mainDir := filepath.Join(t.TempDir(), "main")
	extraDir := filepath.Join(t.TempDir(), "extra")
	writePNG(t, filepath.Join(mainDir, "风景", "a.png"), 20, 10)
	writePNG(t, filepath.Join(extraDir, "风景", "b.png"), 10, 20)

	st, err := NewFileStoreWithOptions(mainDir, Options{
		StartupWait: 10 * time.Second,
		ExtraRoots:  []Root{{Name: "网盘", Path: extraDir}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fs := st.(*fileStore)
	defer fs.Close()

	ctx := context.Background()
	cat := nsSep + "网盘/风景"
	if n, _ := st.CountWith(ctx, cat, OrientationAny); n != 1 {
		t.Fatalf("删除前应有 1 张，实际 %d", n)
	}

	fs.SetExtraRoots(nil)
	// 等重扫完成
	for i := 0; i < 100; i++ {
		if _, err := st.CountWith(ctx, cat, OrientationAny); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := st.CountWith(ctx, cat, OrientationAny); err == nil {
		t.Fatalf("删除后分类不应存在")
	}
	// 主库不受影响
	if n, _ := st.CountWith(ctx, "风景", OrientationAny); n != 1 {
		t.Fatalf("主库应仍有 1 张，实际 %d", n)
	}
	// 路径解析回落到主库（文件不存在 → 服务层 404）
	if p := fs.physPath(cat); p == filepath.Join(extraDir, "风景") {
		t.Fatalf("删除后不应再解析到已移除的库: %s", p)
	}
}
