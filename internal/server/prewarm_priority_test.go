package server

import (
	"context"
	"testing"
	"time"
)

// TestPrewarmYieldsToRequests 有请求在跑时，预热必须让路。
func TestPrewarmYieldsToRequests(t *testing.T) {
	s := newGalleryServer(t, 5, nil)
	s.mem.NoteRequest() // 模拟刚刚有请求
	if s.prewarmYield() {
		t.Fatal("刚有请求时预热应让路")
	}
	// 让"最近请求"过期
	time.Sleep(1100 * time.Millisecond)
	if !s.prewarmYield() {
		t.Fatal("请求空闲后预热应恢复")
	}
}

// TestPrewarmGalleryYieldsWhenRendering 有请求正在渲染图库时，预热不抢名额。
func TestPrewarmGalleryYieldsWhenRendering(t *testing.T) {
	s := newGalleryServer(t, 5, nil)
	if !s.galleryGate.tryAcquire() {
		t.Fatal("占位失败")
	}
	defer s.galleryGate.release()
	if s.galleryGate.idleForPrewarm() {
		t.Fatal("有请求渲染时不应视为空闲")
	}
	if err := s.prewarmGallery("big"); err == nil {
		t.Fatal("渲染忙时预热应返回 busy 而不是硬抢")
	}
}

// TestPrewarmLowBudget 预热最多占一半像素预算，另一半留给请求。
func TestPrewarmLowBudget(t *testing.T) {
	s := newGalleryServer(t, 5, nil)
	capMP := s.previewGate.cap
	// 先用请求占掉 60% 预算
	used := capMP * 6 / 10
	if !s.previewGate.tryAcquire(used) {
		t.Fatal("请求占位失败")
	}
	defer s.previewGate.release(used)
	// 预热再申请 10% 会超过一半水位 → 拒绝
	if s.previewGate.tryAcquireLow(capMP/10+1, 0.5) {
		t.Fatal("超过一半水位时预热不应获得预算")
	}
	// 释放后应可获取小额
	s.previewGate.release(used)
	if !s.previewGate.tryAcquireLow(1, 0.5) {
		t.Fatal("空闲时预热应能获取少量预算")
	}
	_ = context.Background()
}
