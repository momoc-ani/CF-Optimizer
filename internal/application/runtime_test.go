package application

import (
	"encoding/json"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/ranges"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

func TestConfigForStoredReceiptsEnablesOnlyRequiredAdapters(t *testing.T) {
	cfg := config.Default()
	cfg.Proxy.Mihomo.ProviderFile = "/tmp/cf-optimizer-mihomo.yaml"
	receipts, err := json.Marshal(proxy.ApplyResult{Receipts: []proxy.Receipt{
		{Adapter: cleanupAdapterGeneric},
		{Adapter: cleanupAdapterMihomo},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cleanupConfig, err := configForStoredReceipts(cfg, store.State{Policy: &store.PolicySnapshot{Receipts: receipts}})
	if err != nil {
		t.Fatal(err)
	}
	if !cleanupConfig.Network.ManageRoutes || !cleanupConfig.Proxy.Generic.Enabled || !cleanupConfig.Proxy.Mihomo.Enabled {
		t.Fatalf("required cleanup adapters were not enabled: %#v", cleanupConfig)
	}
	if cleanupConfig.Proxy.SingBox.Enabled || cleanupConfig.Proxy.Xray.Enabled || cleanupConfig.Proxy.External.Enabled || cleanupConfig.Hosts.Enabled {
		t.Fatalf("unrelated cleanup adapters were enabled: %#v", cleanupConfig)
	}
}

func TestConfigForStoredReceiptsIncludesPendingTransactionAdapters(t *testing.T) {
	receipts, err := json.Marshal(proxy.ApplyResult{Receipts: []proxy.Receipt{{Adapter: cleanupAdapterMihomo}}})
	if err != nil {
		t.Fatal(err)
	}
	state := store.State{PendingPolicy: store.NewPolicyTransaction(time.Now(), json.RawMessage(`{}`), receipts)}
	cleanupConfig, err := configForStoredReceipts(config.Default(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !cleanupConfig.Proxy.Mihomo.Enabled || cleanupConfig.Proxy.Generic.Enabled {
		t.Fatalf("pending transaction adapters were not isolated: %#v", cleanupConfig.Proxy)
	}
}

func TestConfigForStoredReceiptsRejectsUnknownAdapter(t *testing.T) {
	receipts, err := json.Marshal(proxy.ApplyResult{Receipts: []proxy.Receipt{{Adapter: "unknown-adapter"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configForStoredReceipts(config.Default(), store.State{Policy: &store.PolicySnapshot{Receipts: receipts}}); err == nil {
		t.Fatal("expected unsupported stored adapter to be rejected")
	}
}

func TestConfigForDetectedAdaptersUsesManageableDiscoveredMihomo(t *testing.T) {
	detections := map[string]proxy.Detection{
		cleanupAdapterMihomo: {Present: true, Endpoint: "http://127.0.0.1:19097"},
	}
	cfg := config.Default()
	disabled := configForDetectedAdapters(cfg, detections)
	if disabled.Proxy.Mihomo.Enabled || disabled.Proxy.Mihomo.Controller != cfg.Proxy.Mihomo.Controller {
		t.Fatalf("只读发现不应启用 Mihomo 写入：%#v", disabled.Proxy.Mihomo)
	}
	cfg.Proxy.Mihomo.ProviderFile = filepath.Join(t.TempDir(), "provider.yaml")
	cfg.Proxy.Mihomo.ReloadConfig = filepath.Join(t.TempDir(), "config.yaml")
	detection := detections[cleanupAdapterMihomo]
	detection.Manageable = true
	detections[cleanupAdapterMihomo] = detection
	enabled := configForDetectedAdapters(cfg, detections)
	if !enabled.Proxy.Mihomo.Enabled || enabled.Proxy.Mihomo.Controller != detections[cleanupAdapterMihomo].Endpoint {
		t.Fatalf("已启用 Mihomo 未使用自动发现端点：%#v", enabled.Proxy.Mihomo)
	}
}

func TestAllCloudflareAddressesRejectsMixedOwnership(t *testing.T) {
	snapshot := ranges.Snapshot{IPv4: []string{"104.16.0.0/13"}, IPv6: []string{"2606:4700::/32"}}
	if !allCloudflareAddresses(snapshot, []netip.Addr{netip.MustParseAddr("104.17.158.152"), netip.MustParseAddr("2606:4700::1")}) {
		t.Fatal("expected verified Cloudflare addresses to be accepted")
	}
	if allCloudflareAddresses(snapshot, []netip.Addr{netip.MustParseAddr("104.17.158.152"), netip.MustParseAddr("8.8.8.8")}) {
		t.Fatal("mixed ownership must be rejected")
	}
	if allCloudflareAddresses(snapshot, nil) {
		t.Fatal("empty address set must be rejected")
	}
}

func TestTrimDiscoveredDomainsPreservesActiveAndNewestRecords(t *testing.T) {
	now := time.Now().UTC()
	domains := map[string]store.DomainDiscovery{
		"active.example": {Domain: "active.example", Active: true, LastSeenAt: now.Add(-time.Hour)},
		"old.example":    {Domain: "old.example", LastSeenAt: now.Add(-2 * time.Hour)},
		"new.example":    {Domain: "new.example", LastSeenAt: now},
	}
	trimDiscoveredDomains(domains, 2)
	if _, exists := domains["old.example"]; exists || len(domains) != 2 {
		t.Fatalf("oldest inactive discovery was not evicted: %#v", domains)
	}
	if _, exists := domains["active.example"]; !exists {
		t.Fatalf("active discovery was evicted: %#v", domains)
	}
}

func TestDomainPolicyNeedsRefreshFollowsAutomaticAllocationSwitches(t *testing.T) {
	cfg := config.Default()
	cfg.Acceleration.Enabled = true
	cfg.Acceleration.AutoDiscover = true
	cfg.Acceleration.AutoApply = true
	cfg.Acceleration.ManualDomains = []string{"manual.example"}
	state := store.State{
		DiscoveredDomains: map[string]store.DomainDiscovery{
			"auto.example": {
				Domain: "auto.example", CloudflareVerified: true, PreflightVerified: true, Active: true,
				LastResolvedAddresses: []string{"104.18.1.10"},
			},
		},
		Policy: &store.PolicySnapshot{DomainMappings: []store.DomainMappingSnapshot{
			{Domain: "manual.example", Addresses: []string{"1.1.1.1"}},
		}},
	}
	if domainPolicyNeedsRefresh(cfg, state) {
		t.Fatal("an exhausted pool may leave an automatic domain unassigned without repeated refresh")
	}
	state.Policy.DomainMappings = append(state.Policy.DomainMappings, store.DomainMappingSnapshot{Domain: "auto.example", Addresses: []string{"1.1.1.2"}})
	if domainPolicyNeedsRefresh(cfg, state) {
		t.Fatal("matching manual and automatic allocations should not refresh")
	}
	state.Policy.DomainMappings[1].Addresses = []string{"1.1.1.1"}
	if !domainPolicyNeedsRefresh(cfg, state) {
		t.Fatal("shared legacy domain address should force a new allocation")
	}
	state.Policy.DomainMappings[1].Addresses = []string{"1.1.1.2"}
	cfg.Acceleration.AutoDiscover = false
	if !domainPolicyNeedsRefresh(cfg, state) {
		t.Fatal("disabling automatic discovery should remove an old automatic allocation")
	}
	state.Policy.DomainMappings = state.Policy.DomainMappings[:1]
	if domainPolicyNeedsRefresh(cfg, state) {
		t.Fatal("manual-only allocation should remain valid when automatic discovery is disabled")
	}
}

func TestDomainDiscoverySnapshotReturnsSanitizedPolicyEvidence(t *testing.T) {
	stateStore, err := store.Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	appliedAt := time.Now().UTC().Truncate(time.Second)
	receipts, err := json.Marshal(proxy.ApplyResult{Receipts: []proxy.Receipt{
		{Adapter: cleanupAdapterMihomo, Payload: json.RawMessage(`{"secret_backup":"do-not-expose"}`)},
		{Adapter: cleanupAdapterGeneric},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Update(func(state *store.State) error {
		state.DiscoveredDomains["ani.momoc.top"] = store.DomainDiscovery{
			Domain: "ani.momoc.top", CloudflareVerified: true, PreflightVerified: true,
			LastError: "自动应用失败: an optimization run is already active",
		}
		state.Policy = &store.PolicySnapshot{
			DomainMappings: []store.DomainMappingSnapshot{{Domain: "ani.momoc.top", Addresses: []string{"104.21.94.176"}}},
			Receipts:       receipts,
			AppliedAt:      appliedAt,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Acceleration.ManualDomains = []string{"ani.momoc.top"}
	runtimeState := &Runtime{Config: cfg, Store: stateStore}
	result := runtimeState.domainDiscoverySnapshot()
	if len(result.Domains) != 1 {
		t.Fatalf("unexpected acceleration domains: %#v", result.Domains)
	}
	domain := result.Domains[0]
	if domain.Domain != "ani.momoc.top" || !domain.Active || domain.LastError != "" || len(domain.AcceleratedAddresses) != 1 || domain.AcceleratedAddresses[0] != "104.21.94.176" {
		t.Fatalf("active domain mapping evidence is incomplete: %#v", domain)
	}
	if len(domain.VerifiedAdapters) != 2 || domain.VerifiedAdapters[0] != cleanupAdapterGeneric || domain.VerifiedAdapters[1] != cleanupAdapterMihomo || !domain.AppliedAt.Equal(appliedAt) {
		t.Fatalf("verified adapter evidence is incomplete: %#v", domain)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "do-not-expose") || strings.Contains(string(encoded), "secret_backup") {
		t.Fatal("domain snapshot exposed receipt payload")
	}
}
