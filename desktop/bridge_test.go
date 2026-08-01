package desktop

import (
	"context"
	"strings"
	"testing"
)

func TestBridgeRejectsUnknownMethodBeforeConnecting(t *testing.T) {
	bridge, err := NewBridge("unused-test-endpoint")
	if err != nil {
		t.Fatal(err)
	}
	bridge.Startup(context.Background())
	_, err = bridge.Request("system.shell", nil)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected allowlist error, got %v", err)
	}
}

func TestNewBridgeRequiresEndpoint(t *testing.T) {
	if _, err := NewBridge(""); err == nil {
		t.Fatal("expected endpoint validation error")
	}
}
