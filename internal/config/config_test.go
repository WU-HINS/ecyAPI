package config

import "testing"

// TestMaxDepth 校验 ECY_MAX_DEPTH 的解析与夹取。
func TestMaxDepth(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 3},
		{"1", 1},
		{"5", 5},
		{"8", 8},
		{"99", 8}, // 超过上限夹到 8
		{"0", 3},  // 非正数回退默认
		{"-2", 3},
		{"abc", 3}, // 非法值回退默认
	}
	for _, c := range cases {
		t.Setenv("ECY_MAX_DEPTH", c.env)
		if got := Load().MaxDepth; got != c.want {
			t.Errorf("ECY_MAX_DEPTH=%q → %d，期望 %d", c.env, got, c.want)
		}
	}
}

// TestMemoryLimits 图库上限与预览预算的解析与夹取。
func TestMemoryLimits(t *testing.T) {
	cases := []struct {
		env              string
		gallery, preview int
	}{
		{"1", 1, 8},
		{"32", 32, 32},
		{"100000", 5000, 512},
	}
	for _, c := range cases {
		t.Setenv("ECY_GALLERY_MAX", c.env)
		t.Setenv("ECY_PREVIEW_MP", c.env)
		cfg := Load()
		if cfg.GalleryMax != c.gallery || cfg.PreviewMP != c.preview {
			t.Errorf("env=%q → gallery=%d preview=%d，期望 %d/%d",
				c.env, cfg.GalleryMax, cfg.PreviewMP, c.gallery, c.preview)
		}
	}

	// 未显式设置时：图库展示全部（0），预览预算按堆上限自适应
	t.Setenv("ECY_GALLERY_MAX", "")
	t.Setenv("ECY_PREVIEW_MP", "")
	t.Setenv("ECY_MEMORY_LIMIT", "")
	def := Load()
	if def.GalleryMax != 0 {
		t.Errorf("默认图库上限应为 0，得到 %d", def.GalleryMax)
	}
	if def.PreviewMP != previewBudgetMP(def.MemoryLimit) {
		t.Errorf("预览预算应随堆上限自适应：got %d want %d", def.PreviewMP, previewBudgetMP(def.MemoryLimit))
	}
	if def.PreviewMP < 8 || def.PreviewMP > 64 {
		t.Errorf("预览预算应在 8~64，得到 %d", def.PreviewMP)
	}
	// 非法值同样回退到自适应值
	t.Setenv("ECY_GALLERY_MAX", "abc")
	t.Setenv("ECY_PREVIEW_MP", "-5")
	bad := Load()
	if bad.GalleryMax != 0 || bad.PreviewMP != previewBudgetMP(bad.MemoryLimit) {
		t.Errorf("非法值应回退默认：gallery=%d preview=%d", bad.GalleryMax, bad.PreviewMP)
	}

	// 图库并发与排队超时
	t.Setenv("ECY_GALLERY_CONCURRENCY", "99")
	t.Setenv("ECY_GALLERY_WAIT", "0")
	cfg := Load()
	if cfg.GalleryConcurrency != 16 {
		t.Errorf("并发上限应夹到 16，得到 %d", cfg.GalleryConcurrency)
	}
	if cfg.GalleryWaitSeconds != 5 {
		t.Errorf("0 应回退默认 5 秒，得到 %d", cfg.GalleryWaitSeconds)
	}
}
