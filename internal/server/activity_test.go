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
