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

func TestActivateSessionBroadcastsConfigChange(t *testing.T) {
	runtimeState := &Runtime{Config: config.Default()}
	changes := runtimeState.ConfigChanges()
	next := config.Default()
	next.Schedule.Interval = config.Duration(7 * time.Hour)
	runtimeState.ActivateSession(RuntimeSession{Config: next})
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("runtime config change was not broadcast")
	}
	if runtimeState.View().Config.Schedule.Interval != next.Schedule.Interval {
		t.Fatal("activated session did not replace runtime configuration")
	}
}

func TestDesiredPolicyFromSnapshotPreservesVerifiedMapping(t *testing.T) {
	appliedAt := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	snapshot := &store.PolicySnapshot{
		IPv4CIDRs: []string{"104.25.241.29/32"}, Domains: []string{"ani.momoc.top"},
		DomainMappings: []store.DomainMappingSnapshot{{Domain: "ani.momoc.top", Addresses: []string{"104.25.241.29"}}},
		AppliedAt:      appliedAt,
	}
	desired, exists, err := desiredPolicyFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || desired.Revision == "" || !desired.AppliedAt.Equal(appliedAt) {
		t.Fatalf("unexpected desired policy metadata: %#v", desired)
	}
	if len(desired.Policy.DomainMappings) != 1 || desired.Policy.DomainMappings[0].Addresses[0] != "104.25.241.29" {
		t.Fatalf("stored mapping was recalculated instead of preserved: %#v", desired.Policy.DomainMappings)
	}
	second, _, err := desiredPolicyFromSnapshot(snapshot)
	if err != nil || second.Revision != desired.Revision {
		t.Fatalf("desired policy revision is not deterministic: first=%q second=%q err=%v", desired.Revision, second.Revision, err)
	}
}

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

func TestConfigForDetectedAdaptersKeepsControlOnlyDiscoveredMihomo(t *testing.T) {
	detections := map[string]proxy.Detection{
		cleanupAdapterMihomo: {Present: true, Endpoint: "http://127.0.0.1:19097"},
	}
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	controlOnly := configForDetectedAdapters(cfg, detections)
	if !controlOnly.Proxy.Mihomo.Enabled || controlOnly.Proxy.Mihomo.Controller != detections[cleanupAdapterMihomo].Endpoint || controlOnly.Proxy.Mihomo.ReloadConfig != "" {
		t.Fatalf("可访问控制端应保留 IP/进程规则能力：%#v", controlOnly.Proxy.Mihomo)
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

func TestMergeDetectedMihomoConfigExposesEffectiveController(t *testing.T) {
	persisted := config.Default()
	persisted.Proxy.AutoDetect = true
	effective := persisted
	effective.Proxy.Mihomo.Enabled = true
	effective.Proxy.Mihomo.Controller = "http://127.0.0.1:9097"
	effective.Proxy.Mihomo.ProviderFile = filepath.Join(t.TempDir(), "provider.yaml")
	effective.Proxy.Mihomo.ReloadConfig = filepath.Join(t.TempDir(), "config.yaml")

	merged := mergeDetectedMihomoConfig(persisted, effective, true)
	if !merged.Proxy.Mihomo.Enabled || merged.Proxy.Mihomo.Controller != "http://127.0.0.1:9097" || merged.Proxy.Mihomo.ProviderFile != effective.Proxy.Mihomo.ProviderFile {
		t.Fatalf("effective Mihomo endpoint was not exposed: %#v", merged.Proxy.Mihomo)
	}
	unchanged := mergeDetectedMihomoConfig(persisted, effective, false)
	if unchanged.Proxy.Mihomo.Enabled || unchanged.Proxy.Mihomo.Controller != persisted.Proxy.Mihomo.Controller {
		t.Fatalf("disabled auto-detection must not change persisted config view: %#v", unchanged.Proxy.Mihomo)
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

func TestDomainPolicyNeedsRefreshDoesNotBootstrapMissingPolicy(t *testing.T) {
	cfg := config.Default()
	cfg.Acceleration.Enabled = true
	cfg.Acceleration.AutoDiscover = true
	cfg.Acceleration.AutoApply = true
	state := store.State{DiscoveredDomains: map[string]store.DomainDiscovery{
		"auto.example": {
			Domain: "auto.example", CloudflareVerified: true, PreflightVerified: true, Active: true,
			LastResolvedAddresses: []string{"104.18.1.10"},
		},
	}}
	if domainPolicyNeedsRefresh(cfg, state) {
		t.Fatal("automatic discovery must not bootstrap a policy before a full optimization is verified")
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
	if result.Discovered != 1 || len(result.Domains) != 1 {
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
