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
