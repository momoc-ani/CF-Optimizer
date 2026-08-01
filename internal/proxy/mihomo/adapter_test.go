package mihomo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

func TestAdapterApplyVerifyRollbackAndIdempotency(t *testing.T) {
	secret := "test-secret"
	rules := []map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+secret {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/version":
			_, _ = response.Write([]byte(`{"version":"1.19.0"}`))
		case "/configs":
			response.WriteHeader(http.StatusNoContent)
		case "/rules":
			_ = json.NewEncoder(response).Encode(map[string]any{"rules": rules})
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	providerPath := filepath.Join(directory, "cf-optimizer.yaml")
	previous := []byte("payload: []\n")
	if err := os.WriteFile(providerPath, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().Proxy.Mihomo
	cfg.Enabled = true
	cfg.Controller = server.URL
	cfg.Secret = secret
	cfg.ProviderFile = providerPath
	cfg.ReloadConfig = filepath.Join(directory, "config.yaml")
	cfg.Timeout = config.Duration(time.Second)
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := (proxy.DirectPolicy{IPv4CIDRs: []string{"1.1.1.1/32"}, Domains: []string{"example.com"}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	managedRules := rulesForPolicy(policy)
	for _, rawRule := range managedRules {
		parts := splitRule(rawRule)
		rules = append(rules, map[string]string{"type": parts[0], "payload": parts[1], "proxy": "DIRECT"})
	}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Changed {
		t.Fatal("first apply should change the provider")
	}
	if err := adapter.Verify(context.Background(), policy, receipt); err != nil {
		t.Fatal(err)
	}
	secondPlan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	secondReceipt, err := adapter.Apply(context.Background(), secondPlan)
	if err != nil {
		t.Fatal(err)
	}
	if secondReceipt.Changed {
		t.Fatal("identical apply must be idempotent")
	}
	if err := adapter.Rollback(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Rollback(context.Background(), receipt); err != nil {
		t.Fatalf("rollback should be idempotent: %v", err)
	}
	restored, err := os.ReadFile(providerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(previous) {
		t.Fatalf("provider was not restored: %q", restored)
	}
}

func splitRule(rule string) []string {
	for index := 0; index < len(rule); index++ {
		if rule[index] == ',' {
			for next := index + 1; next < len(rule); next++ {
				if rule[next] == ',' {
					return []string{rule[:index], rule[index+1 : next]}
				}
			}
		}
	}
	return nil
}
