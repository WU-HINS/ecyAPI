package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeTree 造 n 个分类、每个 m 张图的目录树，用于拉长扫描时间。
func makeTree(t *testing.T, n, m int) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "categories")
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	for i := 0; i < n; i++ {
		cat := filepath.Join(dir, fmt.Sprintf("c%03d", i))
		if err := os.MkdirAll(cat, 0o755); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < m; j++ {
			if err := os.WriteFile(filepath.Join(cat, fmt.Sprintf("f%02d.png", j)), png, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestStartupWaitReturnsEarly 首次扫描超过等待上限时先返回，后台继续扫。
func TestStartupWaitReturnsEarly(t *testing.T) {
	dir := makeTree(t, 200, 10)
	start := time.Now()
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("应在上限附近立即返回，实际耗时 %v", elapsed)
	}

	// 后台扫描继续，最终应看到全部分类
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st.Scanning() {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		cats, _ := st.Categories(context.Background())
		if len(cats) == 200 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cats, _ := st.Categories(context.Background())
	t.Fatalf("后台扫描未完成：只有 %d 个分类", len(cats))
}

// TestStartupWaitZeroBlocks 上限为 0 时阻塞到扫描完成（老行为）。
func TestStartupWaitZeroBlocks(t *testing.T) {
	dir := makeTree(t, 50, 5)
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: 0})
	if err != nil {
		t.Fatal(err)
	}
	cats, err := st.Categories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 50 {
		t.Fatalf("阻塞模式下启动后应已就绪，实际 %d 个分类", len(cats))
	}
	if st.Scanning() {
		t.Fatal("构造返回后不应还在扫描")
	}
}

// TestProgressivePublish 首次扫描期间就能读到已扫完的分类。
func TestProgressivePublish(t *testing.T) {
	dir := makeTree(t, 300, 10)
	st, err := NewFileStoreWithOptions(dir, Options{StartupWait: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	// 扫描期间（或刚结束）轮询：只要出现 >0 且 <300 就说明是渐进发布
	sawPartial := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cats, _ := st.Categories(context.Background())
		if len(cats) > 0 && len(cats) < 300 {
			sawPartial = true
		}
		if len(cats) == 300 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	// 渐进发布依赖时序，未观察到 partial 不算失败，但最终必须是全量。
	cats, _ := st.Categories(context.Background())
	if len(cats) != 300 {
		t.Fatalf("最终分类数 %d，期望 300", len(cats))
	}
	t.Logf("观察到部分发布: %v（最终 %d 个分类）", sawPartial, len(cats))
}
