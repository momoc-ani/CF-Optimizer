//go:build !darwin

package main

import "testing"

// TestPrepareTrayStartOther 验证 Windows 与 Linux 保持 Wails 启动后的托盘时序。
func TestPrepareTrayStartOther(t *testing.T) {
	callCount := 0
	deferredStart := prepareTrayStart(func() {
		callCount++
	})

	if callCount != 0 {
		t.Fatalf("prepareTrayStart() call count = %d, want 0", callCount)
	}
	if deferredStart == nil {
		t.Fatal("prepareTrayStart() did not return the deferred start")
	}
	deferredStart()
	if callCount != 1 {
		t.Fatalf("deferred start call count = %d, want 1", callCount)
	}
}
