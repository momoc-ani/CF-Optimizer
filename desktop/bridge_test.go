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

func TestBridgeAllowsAccelerationDomainMethods(t *testing.T) {
	for _, method := range []string{
		"acceleration.domain_test",
		"acceleration.domain_apply",
	} {
		if _, allowed := allowedMethods[method]; !allowed {
			t.Errorf("%s should be available to the desktop UI", method)
		}
	}
}

func TestBridgeAllowsLatestBenchmarkRead(t *testing.T) {
	if _, allowed := allowedMethods["history.latest"]; !allowed {
		t.Fatal("history.latest should be available to the desktop UI")
	}
}
