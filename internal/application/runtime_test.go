package application

import (
	"encoding/json"
	"testing"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
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

func TestConfigForDetectedAdaptersUsesDiscoveredMihomoEndpointOnlyWhenEnabled(t *testing.T) {
	detections := map[string]proxy.Detection{
		cleanupAdapterMihomo: {Present: true, Endpoint: "http://127.0.0.1:19097"},
	}
	cfg := config.Default()
	disabled := configForDetectedAdapters(cfg, detections)
	if disabled.Proxy.Mihomo.Enabled || disabled.Proxy.Mihomo.Controller != cfg.Proxy.Mihomo.Controller {
		t.Fatalf("只读发现不应启用 Mihomo 写入：%#v", disabled.Proxy.Mihomo)
	}
	cfg.Proxy.Mihomo.Enabled = true
	enabled := configForDetectedAdapters(cfg, detections)
	if !enabled.Proxy.Mihomo.Enabled || enabled.Proxy.Mihomo.Controller != detections[cleanupAdapterMihomo].Endpoint {
		t.Fatalf("已启用 Mihomo 未使用自动发现端点：%#v", enabled.Proxy.Mihomo)
	}
}
