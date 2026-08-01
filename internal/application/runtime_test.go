package application

import (
	"encoding/json"
	"net/netip"
	"path/filepath"
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
