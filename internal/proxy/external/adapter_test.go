package external

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

func TestExternalAdapterLifecycle(t *testing.T) {
	t.Setenv("CF_OPTIMIZER_HELPER_PROCESS", "1")
	cfg := config.ExternalProxyConfig{
		Enabled: true, Executable: os.Args[0], Args: []string{"-test.run=TestExternalHelperProcess"}, Timeout: config.Duration(2 * time.Second),
	}
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	policy := proxy.DirectPolicy{IPv4CIDRs: []string{"1.1.1.1/32"}}
	detection, err := adapter.Detect(context.Background())
	if err != nil || !detection.Present {
		t.Fatalf("detection failed: %#v, %v", detection, err)
	}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Verify(context.Background(), policy, receipt); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Rollback(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
}

func TestExternalHelperProcess(t *testing.T) {
	if os.Getenv("CF_OPTIMIZER_HELPER_PROCESS") != "1" {
		return
	}
	requestBody, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	var request rpcRequest
	if json.Unmarshal(requestBody, &request) != nil {
		os.Exit(3)
	}
	var result any
	switch request.Method {
	case "detect":
		result = proxy.Detection{Present: true, Version: "test"}
	case "plan":
		result = map[string]any{"change": "test"}
	case "apply":
		result = map[string]any{"rollback_token": "test"}
	case "verify":
		result = map[string]any{"verified": true}
	case "rollback":
		result = map[string]any{"rolled_back": true}
	default:
		os.Exit(4)
	}
	response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	_, _ = os.Stdout.Write(response)
	os.Exit(0)
}
