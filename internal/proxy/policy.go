package proxy

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"sort"
	"strings"
)

// DirectPolicy 是所有代理内核共享的最小直连策略模型。
type DirectPolicy struct {
	Processes      []string        `json:"processes"`
	IPv4CIDRs      []string        `json:"ipv4_cidrs"`
	IPv6CIDRs      []string        `json:"ipv6_cidrs"`
	Domains        []string        `json:"domains"`
	DomainMappings []DomainMapping `json:"domain_mappings"`
}

// DomainMapping 将一个精确 FQDN 映射到已预检的优选地址集合。
type DomainMapping struct {
	Domain    string   `json:"domain"`
	Addresses []string `json:"addresses"`
}

// Normalize 校验地址族和可注入文本，并返回稳定排序、去重后的策略。
func (p DirectPolicy) Normalize() (DirectPolicy, error) {
	result := DirectPolicy{}
	for _, raw := range p.IPv4CIDRs {
		prefix, err := normalizePrefix(raw, true)
		if err != nil {
			return DirectPolicy{}, fmt.Errorf("invalid IPv4 CIDR %q: %w", raw, err)
		}
		result.IPv4CIDRs = append(result.IPv4CIDRs, prefix)
	}
	for _, raw := range p.IPv6CIDRs {
		prefix, err := normalizePrefix(raw, false)
		if err != nil {
			return DirectPolicy{}, fmt.Errorf("invalid IPv6 CIDR %q: %w", raw, err)
		}
		result.IPv6CIDRs = append(result.IPv6CIDRs, prefix)
	}
	for _, process := range p.Processes {
		process = strings.TrimSpace(process)
		if process == "" || filepath.Base(process) != process || strings.ContainsAny(process, "\r\n,\x00") {
			return DirectPolicy{}, fmt.Errorf("unsafe process name %q", process)
		}
		result.Processes = append(result.Processes, process)
	}
	for _, domain := range p.Domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if err := validateDomain(domain); err != nil {
			return DirectPolicy{}, fmt.Errorf("invalid domain %q: %w", domain, err)
		}
		result.Domains = append(result.Domains, domain)
	}
	mappings := make(map[string][]string, len(p.DomainMappings))
	for _, mapping := range p.DomainMappings {
		domain := strings.ToLower(strings.TrimSpace(mapping.Domain))
		if strings.HasPrefix(domain, "+.") || strings.HasPrefix(domain, "*.") {
			return DirectPolicy{}, fmt.Errorf("domain mapping %q must use an exact FQDN", mapping.Domain)
		}
		if err := validateDomain(domain); err != nil {
			return DirectPolicy{}, fmt.Errorf("invalid domain mapping %q: %w", mapping.Domain, err)
		}
		if len(mapping.Addresses) == 0 {
			return DirectPolicy{}, fmt.Errorf("domain mapping %q has no address", mapping.Domain)
		}
		for _, rawAddress := range mapping.Addresses {
			address, err := netip.ParseAddr(strings.TrimSpace(rawAddress))
			if err != nil || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
				return DirectPolicy{}, fmt.Errorf("domain mapping %q contains an unsafe address %q", mapping.Domain, rawAddress)
			}
			mappings[domain] = append(mappings[domain], address.Unmap().String())
		}
	}
	for domain, addresses := range mappings {
		result.DomainMappings = append(result.DomainMappings, DomainMapping{Domain: domain, Addresses: normalizeList(addresses)})
	}
	sort.Slice(result.DomainMappings, func(i, j int) bool { return result.DomainMappings[i].Domain < result.DomainMappings[j].Domain })
	result.IPv4CIDRs = normalizeList(result.IPv4CIDRs)
	result.IPv6CIDRs = normalizeList(result.IPv6CIDRs)
	result.Processes = normalizeList(result.Processes)
	result.Domains = normalizeList(result.Domains)
	if len(result.IPv4CIDRs)+len(result.IPv6CIDRs)+len(result.Processes)+len(result.Domains)+len(result.DomainMappings) == 0 {
		return DirectPolicy{}, errors.New("direct policy must contain at least one target")
	}
	return result, nil
}

func normalizePrefix(raw string, ipv4 bool) (string, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	prefix = prefix.Masked()
	if prefix.Bits() == 0 || prefix.Addr().Is4() != ipv4 || !prefix.Addr().IsGlobalUnicast() {
		return "", errors.New("CIDR family or address category is not allowed")
	}
	return prefix.String(), nil
}

func validateDomain(domain string) error {
	if len(domain) == 0 || len(domain) > 253 || strings.ContainsAny(domain, "\r\n, /\\\x00") {
		return errors.New("domain contains an unsafe character")
	}
	domain = strings.TrimPrefix(strings.TrimPrefix(domain, "+."), "*.")
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return errors.New("domain label is invalid")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return errors.New("domain label must use ASCII letters, digits, or hyphen")
			}
		}
	}
	return nil
}

func normalizeList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
