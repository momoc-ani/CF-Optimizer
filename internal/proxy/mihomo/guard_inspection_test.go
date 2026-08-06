package mihomo

import (
	"context"
	"encoding/json"
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

func TestInspectManagedPolicyDetectsActiveConfigDrift(t *testing.T) {
	policy, err := (proxy.DirectPolicy{
		Domains: []string{"example.com"}, IPv4CIDRs: []string{"1.1.1.1/32"},
		DomainMappings: []proxy.DomainMapping{{Domain: "example.com", Addresses: []string{"1.1.1.1"}}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	expectedRules := rulesForPolicy(policy)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/configs":
			response.WriteHeader(http.StatusNoContent)
		case "/rules":
			rules := make([]map[string]string, 0, len(expectedRules))
			for _, rule := range expectedRules {
				parts := splitRule(rule)
				rules = append(rules, map[string]string{"type": parts[0], "payload": parts[1], "proxy": "DIRECT"})
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"rules": rules})
		default:
			response.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	cfg := config.Default().Proxy.Mihomo
	cfg.Enabled = true
	cfg.Controller = server.URL
	cfg.ProviderFile = filepath.Join(directory, "provider.yaml")
	cfg.ReloadConfig = filepath.Join(directory, "config.yaml")
	cfg.Timeout = config.Duration(time.Second)
	if err := os.WriteFile(cfg.ReloadConfig, []byte("rules:\n  - MATCH,proxy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	inspection, err := adapter.InspectManagedPolicy(context.Background(), policy)
	if err != nil || !inspection.Healthy {
		t.Fatalf("刚应用的策略应保持健康: %#v, %v", inspection, err)
	}

	content, err := os.ReadFile(cfg.ReloadConfig)
	if err != nil {
		t.Fatal(err)
	}
	drifted := strings.ReplaceAll(string(content), "  example.com: 1.1.1.1\n", "")
	if err := os.WriteFile(cfg.ReloadConfig, []byte(drifted), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err = adapter.InspectManagedPolicy(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Healthy || !containsReason(inspection.DriftReasons, "域名映射 example.com") {
		t.Fatalf("未识别活动配置域名映射漂移: %#v", inspection)
	}
}

func TestProxySettingUsesMihomoLoopbackPort(t *testing.T) {
	for _, raw := range []string{"127.0.0.1:7897", "http=127.0.0.1:7897;https=127.0.0.1:7897", "socks://[::1]:7897"} {
		if !proxySettingUsesPorts(raw, []int{7897}) {
			t.Fatalf("未识别 Mihomo 系统代理端点 %q", raw)
		}
	}
	for _, raw := range []string{"192.168.1.2:7897", "127.0.0.1:7890", "http://proxy.example:7897"} {
		if proxySettingUsesPorts(raw, []int{7897}) {
			t.Fatalf("错误接受非 Mihomo 系统代理端点 %q", raw)
		}
	}
}

func containsReason(reasons []string, expected string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, expected) {
			return true
		}
	}
	return false
}
