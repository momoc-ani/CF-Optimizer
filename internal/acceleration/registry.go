package acceleration

import (
	"sort"
	"strings"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

// EffectiveDomains 合并手动域名与已授权的自动发现域名，并确保排除项最终生效。
func EffectiveDomains(cfg config.Config, state store.State) []string {
	if !cfg.Acceleration.Enabled {
		return nil
	}
	excluded := make(map[string]struct{}, len(cfg.Acceleration.ExcludedDomains))
	for _, domain := range cfg.Acceleration.ExcludedDomains {
		excluded[normalizeDomain(domain)] = struct{}{}
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, len(cfg.Acceleration.ManualDomains)+len(state.DiscoveredDomains))
	appendDomain := func(domain string) {
		domain = normalizeDomain(domain)
		if domain == "" {
			return
		}
		if _, blocked := excluded[domain]; blocked {
			return
		}
		if _, exists := seen[domain]; exists {
			return
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	for _, domain := range cfg.AccelerationDomains() {
		appendDomain(domain)
	}
	if cfg.Acceleration.AutoApply {
		for _, discovery := range state.DiscoveredDomains {
			if discovery.CloudflareVerified && discovery.PreflightVerified && discovery.Active {
				appendDomain(discovery.Domain)
			}
		}
	}
	sort.Strings(result)
	return result
}

func normalizeDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}
