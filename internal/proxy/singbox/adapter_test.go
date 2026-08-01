package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

func TestManagedSingBoxLifecycle(t *testing.T) {
	managedPath := filepath.Join(t.TempDir(), "90-cf-optimizer.json")
	cfg := config.ManagedProxyConfig{ManagedFile: managedPath, DirectOutbound: "direct", Timeout: config.Duration(time.Second)}
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := (proxy.DirectPolicy{
		IPv4CIDRs: []string{"1.1.1.1/32"}, IPv6CIDRs: []string{"2606:4700::1/128"},
		Domains: []string{"example.com"}, Processes: []string{"cf-optimizer"},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
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
	content, err := os.ReadFile(managedPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("managed fragment is invalid JSON: %v", err)
	}
	if err := adapter.Rollback(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Rollback(context.Background(), receipt); err != nil {
		t.Fatalf("rollback should be idempotent: %v", err)
	}
	if _, err := os.Stat(managedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new managed file was not removed: %v", err)
	}
}

func TestManagedSingBoxRollbackChainRestoresOriginalState(t *testing.T) {
	managedPath := filepath.Join(t.TempDir(), "90-cf-optimizer.json")
	adapter, err := New(config.ManagedProxyConfig{ManagedFile: managedPath, DirectOutbound: "direct", Timeout: config.Duration(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	var receipts []proxy.Receipt
	for _, address := range []string{"1.1.1.1/32", "1.0.0.1/32"} {
		policy := proxy.DirectPolicy{IPv4CIDRs: []string{address}}
		plan, err := adapter.Plan(context.Background(), policy)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := adapter.Apply(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, receipt)
	}
	for index := len(receipts) - 1; index >= 0; index-- {
		if err := adapter.Rollback(context.Background(), receipts[index]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(managedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback chain did not restore the missing original file: %v", err)
	}
}
