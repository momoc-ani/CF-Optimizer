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
	for _, discovery := range EffectiveDiscoveries(cfg, state) {
		appendDomain(discovery.Domain)
	}
	sort.Strings(result)
	return result
}

// EffectiveDiscoveries 仅在三项开关同时开启时返回可消费剩余优选 IP 的稳定发现记录。
func EffectiveDiscoveries(cfg config.Config, state store.State) []store.DomainDiscovery {
	if !cfg.Acceleration.Enabled || !cfg.Acceleration.AutoDiscover || !cfg.Acceleration.AutoApply {
		return nil
	}
	excluded := make(map[string]struct{}, len(cfg.Acceleration.ExcludedDomains))
	for _, domain := range cfg.Acceleration.ExcludedDomains {
		excluded[normalizeDomain(domain)] = struct{}{}
	}
	manual := make(map[string]struct{}, len(cfg.AccelerationDomains()))
	for _, domain := range cfg.AccelerationDomains() {
		manual[normalizeDomain(domain)] = struct{}{}
	}
	result := make([]store.DomainDiscovery, 0, len(state.DiscoveredDomains))
	for _, discovery := range state.DiscoveredDomains {
		domain := normalizeDomain(discovery.Domain)
		if domain == "" || !discovery.CloudflareVerified || !discovery.PreflightVerified || !discovery.Active || len(discovery.LastResolvedAddresses) == 0 {
			continue
		}
		if _, blocked := excluded[domain]; blocked {
			continue
		}
		if _, isManual := manual[domain]; isManual {
			continue
		}
		discovery.Domain = domain
		result = append(result, discovery)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Domain < result[j].Domain })
	return result
}

func normalizeDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}
