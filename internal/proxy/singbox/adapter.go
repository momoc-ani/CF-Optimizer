package singbox

import (
	"encoding/json"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/managedfile"
)

// New 创建只写独立 JSON 配置片段的 sing-box 适配器。
func New(cfg config.ManagedProxyConfig) (*managedfile.Adapter, error) {
	capabilities := proxy.Capabilities{Processes: true, IPv4: true, IPv6: true, Domains: true, DomainMappings: true, HotReload: len(cfg.ReloadArgs) > 0, Rollback: true}
	return managedfile.New("sing-box", cfg, format, capabilities)
}

func format(policy proxy.DirectPolicy, directOutbound string) ([]byte, int, error) {
	type rule struct {
		IPCIDR      []string `json:"ip_cidr,omitempty"`
		Domain      []string `json:"domain,omitempty"`
		ProcessName []string `json:"process_name,omitempty"`
		Action      string   `json:"action"`
		Outbound    string   `json:"outbound"`
	}
	var rules []rule
	if len(policy.IPv4CIDRs)+len(policy.IPv6CIDRs) > 0 {
		rules = append(rules, rule{IPCIDR: append(append([]string{}, policy.IPv4CIDRs...), policy.IPv6CIDRs...), Action: "route", Outbound: directOutbound})
	}
	if len(policy.Domains) > 0 {
		rules = append(rules, rule{Domain: policy.Domains, Action: "route", Outbound: directOutbound})
	}
	if len(policy.Processes) > 0 {
		rules = append(rules, rule{ProcessName: policy.Processes, Action: "route", Outbound: directOutbound})
	}
	type dnsServer struct {
		Type       string              `json:"type"`
		Tag        string              `json:"tag"`
		Predefined map[string][]string `json:"predefined"`
	}
	type dnsRule struct {
		Domain []string `json:"domain"`
		Action string   `json:"action"`
		Server string   `json:"server"`
	}
	document := struct {
		DNS struct {
			Servers []dnsServer `json:"servers,omitempty"`
			Rules   []dnsRule   `json:"rules,omitempty"`
		} `json:"dns,omitempty"`
		Route struct {
			Rules []rule `json:"rules"`
		} `json:"route"`
	}{}
	if len(policy.DomainMappings) > 0 {
		predefined := make(map[string][]string, len(policy.DomainMappings))
		domains := make([]string, 0, len(policy.DomainMappings))
		for _, mapping := range policy.DomainMappings {
			predefined[mapping.Domain] = append([]string(nil), mapping.Addresses...)
			domains = append(domains, mapping.Domain)
		}
		document.DNS.Servers = []dnsServer{{Type: "hosts", Tag: "cf-optimizer", Predefined: predefined}}
		document.DNS.Rules = []dnsRule{{Domain: domains, Action: "route", Server: "cf-optimizer"}}
	}
	document.Route.Rules = rules
	content, err := json.MarshalIndent(document, "", "  ")
	return append(content, '\n'), len(rules) + len(policy.DomainMappings), err
}
