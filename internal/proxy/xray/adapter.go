package xray

import (
	"encoding/json"
	"strings"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/managedfile"
)

// New 创建只写独立 JSON 配置片段的 Xray/V2Ray 适配器。
func New(cfg config.ManagedProxyConfig) (*managedfile.Adapter, error) {
	capabilities := proxy.Capabilities{IPv4: true, IPv6: true, Domains: true, HotReload: len(cfg.ReloadArgs) > 0, Rollback: true}
	return managedfile.New("xray", cfg, format, capabilities)
}

func format(policy proxy.DirectPolicy, directOutbound string) ([]byte, int, error) {
	type rule struct {
		Type        string   `json:"type"`
		IP          []string `json:"ip,omitempty"`
		Domain      []string `json:"domain,omitempty"`
		OutboundTag string   `json:"outboundTag"`
	}
	var rules []rule
	if len(policy.IPv4CIDRs)+len(policy.IPv6CIDRs) > 0 {
		rules = append(rules, rule{Type: "field", IP: append(append([]string{}, policy.IPv4CIDRs...), policy.IPv6CIDRs...), OutboundTag: directOutbound})
	}
	if len(policy.Domains) > 0 {
		domains := make([]string, 0, len(policy.Domains))
		for _, domain := range policy.Domains {
			if strings.HasPrefix(domain, "+.") || strings.HasPrefix(domain, "*.") {
				domains = append(domains, "domain:"+domain[2:])
			} else {
				domains = append(domains, "full:"+domain)
			}
		}
		rules = append(rules, rule{Type: "field", Domain: domains, OutboundTag: directOutbound})
	}
	document := struct {
		Routing struct {
			Rules []rule `json:"rules"`
		} `json:"routing"`
	}{}
	document.Routing.Rules = rules
	content, err := json.MarshalIndent(document, "", "  ")
	return append(content, '\n'), len(rules), err
}
