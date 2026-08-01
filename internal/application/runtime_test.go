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
