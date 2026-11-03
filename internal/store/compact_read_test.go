package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestCompactReadParity M2b-3 验收：开关打开后，读路径与内存路径结果一致。
func TestCompactReadParity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data", "categories")
	for c := 0; c < 3; c++ {
		for i := 0; i < 20; i++ {
			writePNG(t, filepath.Join(dir, "c"+pad(c), "p"+pad(i)+".png"), 4, 2)
		}
	}

	// 先建一份索引（走普通内存模式 + 快照）
	st1, err := NewFileStoreWithOptions(dir, Options{StartupWait: 5 * time.Second, SnapshotTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs1 := st1.(*fileStore)
	if err := fs1.saveSnapshot(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cat := "c" + pad(0)
	wantMem, _ := st1.CountWith(ctx, cat, OrientationAny)
	rowsMem, _ := st1.RandomWith(ctx, cat, 10, OrientationAny)
	byIdxMem, err := st1.ByIndexWith(ctx, cat, 3, OrientationAny)
	if err != nil {
		t.Fatal(err)
	}
	fs1.Close()

	// 再以 compact 读路径打开
	st2, err := NewFileStoreWithOptions(dir, Options{
		StartupWait: 5 * time.Second, SnapshotTTL: time.Hour, SnapshotMaxAge: time.Hour, CompactRead: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs2 := st2.(*fileStore)
	defer fs2.Close()
	if fs2.compactReady() == nil {
		t.Fatal("compact 读路径未启用")
	}

	wantCompact, _ := st2.CountWith(ctx, cat, OrientationAny)
	if wantCompact != wantMem {
		t.Fatalf("计数不一致：内存 %d / 紧凑 %d", wantMem, wantCompact)
	}
	if len(rowsMem) != 10 {
		t.Fatalf("内存路径随机返回 %d 条", len(rowsMem))
	}
	rowsCompact, err := st2.RandomWith(ctx, cat, 10, OrientationAny)
	if err != nil || len(rowsCompact) != 10 {
		t.Fatalf("紧凑路径随机返回 %d 条 err=%v", len(rowsCompact), err)
	}
	seen := map[string]bool{}
	for _, r := range rowsCompact {
		if seen[r.Link] {
			t.Fatalf("随机出现重复：%s", r.Link)
		}
		seen[r.Link] = true
		if r.ID == "" || r.Link == "" {
			t.Fatalf("行字段为空：%+v", r)
		}
	}
	byIdxCompact, err := st2.ByIndexWith(ctx, cat, 3, OrientationAny)
	if err != nil || byIdxCompact.Link != byIdxMem.Link {
		t.Fatalf("按序号取图不一致：%q vs %q（err=%v）", byIdxCompact.Link, byIdxMem.Link, err)
	}

	// 方向过滤：随机的 10 条都必须是横向（16x9 的测试图）
	for _, r := range rowsCompact {
		if r.Orientation != OrientationLandscape.String() && r.Orientation != OrientationUnknown.String() {
			t.Fatalf("方向不对：%+v", r)
		}
	}
}
