package server

import (
	"strings"
	"testing"
)

// TestActivityLine 活跃任务日志：空闲不输出，有任务时带任务与占用。
func TestActivityLine(t *testing.T) {
	s := newStaticServer(t, "")
	if line, ok := s.activityLine(); ok {
		t.Fatalf("空闲时不应输出，实际: %s", line)
	}

	done := s.beginRender("pc")
	line, ok := s.activityLine()
	if !ok {
		t.Fatal("渲染中应输出")
	}
	for _, want := range []string{"图库渲染 1", "pc", "堆", "RSS"} {
		if !strings.Contains(line, want) {
			t.Fatalf("缺少 %q: %s", want, line)
		}
	}
	done()

	// 缩略图压缩
	doneP := s.beginPreview(1920*1080, 480)
	line, ok = s.activityLine()
	if !ok || !strings.Contains(line, "缩略图压缩 1") || !strings.Contains(line, "480×1") {
		t.Fatalf("缩略图状态不对: %v %s", ok, line)
	}
	doneP()

	// gzip
	doneG := s.beginGzip()
	line, ok = s.activityLine()
	if !ok || !strings.Contains(line, "gzip 1") {
		t.Fatalf("gzip 状态不对: %v %s", ok, line)
	}
	doneG()

	// 请求计数也算活跃
	doneR := s.noteRequestStart()
	line, ok = s.activityLine()
	if !ok || !strings.Contains(line, "请求 处理中 1") {
		t.Fatalf("请求状态不对: %v %s", ok, line)
	}
	doneR()
}

// TestAccessCurveEvaluate 访问曲线：持续流量按均值评估，尖峰不会被立刻全额放宽。
func TestAccessCurveEvaluate(t *testing.T) {
	var c accessCurve
	// 持续 1000 QPS 五档
	for i := 0; i < 10; i++ {
		c.push(1000)
	}
	if got := c.eval(1000); got < 900 || got > 1100 {
		t.Fatalf("稳定流量评估应≈1000，得到 %.0f", got)
	}
	// 单档尖峰 10000：评估值应高于稳定值但远低于尖峰值
	c.push(10000)
	ev := c.eval(10000)
	if ev <= 1000 {
		t.Fatalf("尖峰后评估应上升，得到 %.0f", ev)
	}
	if ev >= 10000 {
		t.Fatalf("尖峰不应全额计入，得到 %.0f", ev)
	}
	// 回落：连续低流量后评估应降下来
	for i := 0; i < 10; i++ {
		c.push(0)
	}
	if got := c.eval(0); got > 1000 {
		t.Fatalf("流量回落后评估应下降，得到 %.0f", got)
	}
}
