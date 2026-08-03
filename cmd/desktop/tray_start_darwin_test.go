//go:build darwin

package main

import "testing"

// TestPrepareTrayStartDarwin 验证 macOS 在 Wails 主循环前同步初始化托盘。
func TestPrepareTrayStartDarwin(t *testing.T) {
	callCount := 0
	deferredStart := prepareTrayStart(func() {
		callCount++
	})

	if callCount != 1 {
		t.Fatalf("prepareTrayStart() call count = %d, want 1", callCount)
	}
	if deferredStart != nil {
		t.Fatal("prepareTrayStart() returned a deferred start on darwin")
	}
}

// TestPrepareTrayStartDarwinNil 验证缺少启动函数时不会产生延迟调用。
func TestPrepareTrayStartDarwinNil(t *testing.T) {
	if deferredStart := prepareTrayStart(nil); deferredStart != nil {
		t.Fatal("prepareTrayStart(nil) returned a deferred start on darwin")
	}
}
