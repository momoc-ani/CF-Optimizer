package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	if err := os.Chmod(activeConfigPath, 0o644); err != nil {
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
	assertThirdPartyConfigPermission(t, activeConfigPath, 0o644)
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
	assertThirdPartyConfigPermission(t, activeConfigPath, 0o644)
	if _, err := os.Stat(managedMetadataPath(providerPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new managed metadata was not removed: %v", err)
	}
}

func TestCleanupConflictRemovesManagedDescendantWithoutOverwritingOtherConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/configs" {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		response.WriteHeader(http.StatusOK)
	}))
	directory := t.TempDir()
	providerPath := filepath.Join(directory, "cf-optimizer.yaml")
	activeConfigPath := filepath.Join(directory, "config.yaml")
	providerPrevious := []byte("payload: []\n")
	configPrevious := []byte("dns:\n  use-hosts: false\nhosts:\n  existing.example: 9.9.9.9\nrules:\n  - MATCH,proxy\n")
	if err := os.WriteFile(providerPath, providerPrevious, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activeConfigPath, configPrevious, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(activeConfigPath, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().Proxy.Mihomo
	cfg.Enabled = true
	cfg.Controller = server.URL
	cfg.ProviderFile = providerPath
	cfg.ReloadConfig = activeConfigPath
	cfg.Timeout = config.Duration(time.Second)
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	firstPolicy, err := (proxy.DirectPolicy{
		IPv4CIDRs: []string{"1.1.1.1/32"}, Domains: []string{"example.com"},
		DomainMappings: []proxy.DomainMapping{{Domain: "example.com", Addresses: []string{"1.1.1.1"}}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	firstPlan, err := adapter.Plan(context.Background(), firstPolicy)
	if err != nil {
		t.Fatal(err)
	}
	staleReceipt, err := adapter.Apply(context.Background(), firstPlan)
	if err != nil {
		t.Fatal(err)
	}
	secondPolicy, err := (proxy.DirectPolicy{
		IPv4CIDRs: []string{"1.1.1.2/32"}, Domains: []string{"example.com"},
		DomainMappings: []proxy.DomainMapping{{Domain: "example.com", Addresses: []string{"1.1.1.2"}}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := adapter.Plan(context.Background(), secondPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Apply(context.Background(), secondPlan); err != nil {
		t.Fatal(err)
	}
	currentConfig, err := os.ReadFile(activeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	currentConfig = append(currentConfig, []byte("mode: rule\n")...)
	if err := os.WriteFile(activeConfigPath, currentConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	server.Close()
	if err := adapter.CleanupConflict(context.Background(), []proxy.Receipt{staleReceipt}); err != nil {
		t.Fatal(err)
	}
	restoredProvider, err := os.ReadFile(providerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredProvider) != string(providerPrevious) {
		t.Fatalf("non-managed provider baseline was not restored: %s", restoredProvider)
	}
	cleanedConfig, err := os.ReadFile(activeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cleanedText := string(cleanedConfig)
	for _, unwanted := range []string{"example.com", "1.1.1.2/32"} {
		if strings.Contains(cleanedText, unwanted) {
			t.Fatalf("managed value %q remained after cleanup: %s", unwanted, cleanedConfig)
		}
	}
	for _, expected := range []string{"existing.example: 9.9.9.9", "use-hosts: false", "mode: rule", "MATCH,proxy"} {
		if !strings.Contains(cleanedText, expected) {
			t.Fatalf("unrelated config %q was not preserved: %s", expected, cleanedConfig)
		}
	}
	assertThirdPartyConfigPermission(t, activeConfigPath, 0o644)
	if _, err := os.Stat(managedMetadataPath(providerPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed metadata was not removed: %v", err)
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

func TestCleanupManagedConfigRejectsNonAddressHostEdit(t *testing.T) {
	metadataContent, err := json.Marshal(managedMetadata{
		Version:        managedMetadataVersion,
		ManagedDomains: []string{"example.com"},
		OriginalHosts:  map[string]originalHostValue{"example.com": {Exists: false}},
		OriginalRules:  map[string]bool{},
	})
	if err != nil {
		t.Fatal(err)
	}
	configContent := []byte("hosts:\n  example.com: user-defined.example\nrules: []\n")
	if _, err := cleanupManagedConfig(configContent, metadataContent); err == nil {
		t.Fatal("cleanup overwrote a host value that could not be proven to be managed")
	}
}

func TestControllerUnavailableRejectsNonNetworkProtocolError(t *testing.T) {
	if controllerUnavailable(errors.New("Mihomo reload returned 401")) {
		t.Fatal("HTTP or authentication failure was treated as an offline controller")
	}
}

func TestApplyFailureRestoresThirdPartyConfigPermission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/configs" {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	directory := t.TempDir()
	providerPath := filepath.Join(directory, "cf-optimizer.yaml")
	activeConfigPath := filepath.Join(directory, "config.yaml")
	activePrevious := []byte("rules:\n  - MATCH,proxy\n")
	if err := os.WriteFile(providerPath, []byte("payload: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activeConfigPath, activePrevious, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(activeConfigPath, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().Proxy.Mihomo
	cfg.Enabled = true
	cfg.Controller = server.URL
	cfg.ProviderFile = providerPath
	cfg.ReloadConfig = activeConfigPath
	cfg.Timeout = config.Duration(time.Second)
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := (proxy.DirectPolicy{
		Domains:        []string{"example.com"},
		DomainMappings: []proxy.DomainMapping{{Domain: "example.com", Addresses: []string{"1.1.1.1"}}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Apply(context.Background(), plan); err == nil {
		t.Fatal("Mihomo reload failure must fail apply")
	}
	restored, err := os.ReadFile(activeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(activePrevious) {
		t.Fatalf("active config after failed apply = %q", restored)
	}
	assertThirdPartyConfigPermission(t, activeConfigPath, 0o644)
}

func assertThirdPartyConfigPermission(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("third-party config permission = %o, want %o", info.Mode().Perm(), want)
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
