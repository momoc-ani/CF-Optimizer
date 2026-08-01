package mihomo

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"gopkg.in/yaml.v3"
)

const managedMetadataVersion = 1

type originalHostValue struct {
	Exists bool `json:"exists"`
	Value  any  `json:"value,omitempty"`
}

type managedMetadata struct {
	Version        int                          `json:"version"`
	ManagedDomains []string                     `json:"managed_domains"`
	OriginalHosts  map[string]originalHostValue `json:"original_hosts"`
	ManagedRules   []string                     `json:"managed_rules"`
	OriginalRules  map[string]bool              `json:"original_rules"`
}

// patchManagedConfig 在保留未知字段的前提下写入精确 hosts 映射和置顶 DIRECT 规则。
func patchManagedConfig(configContent, metadataContent []byte, metadataExists bool, policy proxy.DirectPolicy, rules []string) ([]byte, []byte, error) {
	var document map[string]any
	if err := yaml.Unmarshal(configContent, &document); err != nil {
		return nil, nil, fmt.Errorf("decode Mihomo active config: %w", err)
	}
	if document == nil {
		return nil, nil, errors.New("Mihomo active config must be a YAML mapping")
	}
	metadata := managedMetadata{
		Version: managedMetadataVersion, OriginalHosts: map[string]originalHostValue{}, OriginalRules: map[string]bool{},
	}
	if metadataExists {
		if err := json.Unmarshal(metadataContent, &metadata); err != nil {
			return nil, nil, fmt.Errorf("decode Mihomo managed metadata: %w", err)
		}
		if metadata.Version != managedMetadataVersion {
			return nil, nil, fmt.Errorf("unsupported Mihomo managed metadata version %d", metadata.Version)
		}
		if metadata.OriginalHosts == nil {
			metadata.OriginalHosts = map[string]originalHostValue{}
		}
		if metadata.OriginalRules == nil {
			metadata.OriginalRules = map[string]bool{}
		}
	}

	hosts, err := stringMap(document["hosts"])
	if err != nil {
		return nil, nil, fmt.Errorf("decode Mihomo hosts: %w", err)
	}
	newDomains := make(map[string]proxy.DomainMapping, len(policy.DomainMappings))
	for _, mapping := range policy.DomainMappings {
		newDomains[mapping.Domain] = mapping
		if _, tracked := metadata.OriginalHosts[mapping.Domain]; !tracked {
			value, exists := hosts[mapping.Domain]
			metadata.OriginalHosts[mapping.Domain] = originalHostValue{Exists: exists, Value: value}
		}
	}
	for _, domain := range metadata.ManagedDomains {
		if _, stillManaged := newDomains[domain]; stillManaged {
			continue
		}
		original := metadata.OriginalHosts[domain]
		if original.Exists {
			hosts[domain] = original.Value
		} else {
			delete(hosts, domain)
		}
		delete(metadata.OriginalHosts, domain)
	}
	metadata.ManagedDomains = metadata.ManagedDomains[:0]
	for _, mapping := range policy.DomainMappings {
		metadata.ManagedDomains = append(metadata.ManagedDomains, mapping.Domain)
		if len(mapping.Addresses) == 1 {
			hosts[mapping.Domain] = mapping.Addresses[0]
		} else {
			hosts[mapping.Domain] = append([]string(nil), mapping.Addresses...)
		}
	}
	document["hosts"] = hosts

	dns, err := stringMap(document["dns"])
	if err != nil {
		return nil, nil, fmt.Errorf("decode Mihomo DNS config: %w", err)
	}
	dns["use-hosts"] = true
	document["dns"] = dns

	activeRules, err := stringList(document["rules"])
	if err != nil {
		return nil, nil, fmt.Errorf("decode Mihomo rules: %w", err)
	}
	for _, oldRule := range metadata.ManagedRules {
		if !metadata.OriginalRules[oldRule] {
			activeRules = removeString(activeRules, oldRule)
		}
		if !slices.Contains(rules, oldRule) {
			delete(metadata.OriginalRules, oldRule)
		}
	}
	managedPrefix := make([]string, 0, len(rules))
	for _, rule := range rules {
		if _, tracked := metadata.OriginalRules[rule]; !tracked {
			metadata.OriginalRules[rule] = slices.Contains(activeRules, rule)
		}
		if !metadata.OriginalRules[rule] {
			activeRules = removeString(activeRules, rule)
			managedPrefix = append(managedPrefix, rule)
		}
	}
	document["rules"] = append(managedPrefix, activeRules...)
	metadata.ManagedRules = append([]string(nil), rules...)

	patchedConfig, err := yaml.Marshal(document)
	if err != nil {
		return nil, nil, fmt.Errorf("encode Mihomo active config: %w", err)
	}
	patchedMetadata, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode Mihomo managed metadata: %w", err)
	}
	return patchedConfig, append(patchedMetadata, '\n'), nil
}

// stringMap 将 YAML 映射规范为字符串键，拒绝无法安全重写的结构。
func stringMap(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("value is not a mapping")
	}
	return result, nil
}

// stringList 将 YAML 序列规范为字符串列表，拒绝混合类型规则。
func stringList(value any) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	items, ok := value.([]any)
	if !ok {
		if strings, stringOK := value.([]string); stringOK {
			return append([]string(nil), strings...), nil
		}
		return nil, errors.New("value is not a sequence")
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		rule, ok := item.(string)
		if !ok {
			return nil, errors.New("rule is not a string")
		}
		result = append(result, rule)
	}
	return result, nil
}

// removeString 从规则列表中移除所有精确匹配项并保持其余顺序。
func removeString(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

// managedMetadataPath 将回滚元数据放在受管 provider 同目录下。
func managedMetadataPath(providerPath string) string {
	return providerPath + ".metadata.json"
}
