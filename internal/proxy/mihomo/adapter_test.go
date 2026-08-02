package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	activeConfigPath := filepath.Join(directory, "config.yaml")
	previous := []byte("payload: []\n")
	if err := os.WriteFile(providerPath, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	activePrevious := []byte("dns:\n  use-hosts: false\nhosts:\n  existing.example: 9.9.9.9\nrules:\n  - MATCH,proxy\n")
	if err := os.WriteFile(activeConfigPath, activePrevious, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().Proxy.Mihomo
	cfg.Enabled = true
	cfg.Controller = server.URL
	cfg.Secret = secret
	cfg.ProviderFile = providerPath
	cfg.ReloadConfig = activeConfigPath
	cfg.Timeout = config.Duration(time.Second)
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	detection, err := adapter.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if detection.ConfigPath != activeConfigPath {
		t.Fatalf("detected active config = %q, want %q", detection.ConfigPath, activeConfigPath)
	}
	adapter.connectionVerifier = func(context.Context, []proxy.DomainMapping) error { return nil }
	policy, err := (proxy.DirectPolicy{
		IPv4CIDRs: []string{"1.1.1.1/32"}, Domains: []string{"example.com"},
		DomainMappings: []proxy.DomainMapping{{Domain: "example.com", Addresses: []string{"1.1.1.1"}}},
	}).Normalize()
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
	activeContent, err := os.ReadFile(activeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(activeContent), "example.com: 1.1.1.1") || !strings.Contains(string(activeContent), "- DOMAIN,example.com,DIRECT") {
		t.Fatalf("active config was not patched: %s", activeContent)
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
	restoredConfig, err := os.ReadFile(activeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredConfig) != string(activePrevious) {
		t.Fatalf("active config was not restored: %q", restoredConfig)
	}
	if _, err := os.Stat(managedMetadataPath(providerPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new managed metadata was not removed: %v", err)
	}
}

func TestPlanRemovesPreviouslyManagedHostsWhenMappingsBecomeEmpty(t *testing.T) {
	directory := t.TempDir()
	providerPath := filepath.Join(directory, "cf-optimizer.yaml")
	activeConfigPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(providerPath, []byte("payload: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	activeConfig := []byte("hosts:\n  dash.cloudflare.com: 172.66.2.98\nrules:\n  - DOMAIN,dash.cloudflare.com,DIRECT\n  - MATCH,proxy\n")
	if err := os.WriteFile(activeConfigPath, activeConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := managedMetadata{
		Version:        managedMetadataVersion,
		ManagedDomains: []string{"dash.cloudflare.com"},
		OriginalHosts:  map[string]originalHostValue{"dash.cloudflare.com": {Exists: false}},
		ManagedRules:   []string{"DOMAIN,dash.cloudflare.com,DIRECT"},
		OriginalRules:  map[string]bool{"DOMAIN,dash.cloudflare.com,DIRECT": false},
	}
	metadataContent, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedMetadataPath(providerPath), metadataContent, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().Proxy.Mihomo
	cfg.Enabled = true
	cfg.Controller = "http://127.0.0.1:9090"
	cfg.ProviderFile = providerPath
	cfg.ReloadConfig = activeConfigPath
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := (proxy.DirectPolicy{IPv4CIDRs: []string{"172.66.2.98/32"}, Domains: []string{"dash.cloudflare.com"}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	var payload planPayload
	if err := json.Unmarshal(plan.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.ConfigContent) == 0 || strings.Contains(string(payload.ConfigContent), "dash.cloudflare.com: 172.66.2.98") {
		t.Fatalf("stale managed host was not removed: %s", payload.ConfigContent)
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
