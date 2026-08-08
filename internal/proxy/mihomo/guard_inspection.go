package mihomo

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/guard"
	"gopkg.in/yaml.v3"
)

// InspectManagedPolicy 检查受管文件、活动配置及控制面规则是否仍与期望策略一致。
func (a *Adapter) InspectManagedPolicy(ctx context.Context, policy proxy.DirectPolicy) (guard.Inspection, error) {
	normalized, err := policy.Normalize()
	if err != nil {
		return guard.Inspection{}, err
	}
	expectedRules := rulesForPolicy(normalized)
	reasons := make([]string, 0, 8)
	provider, providerErr := os.ReadFile(a.config.ProviderFile)
	if providerErr != nil {
		reasons = append(reasons, "受管规则文件不存在或不可读")
	} else if !providerContainsRules(provider, expectedRules) {
		reasons = append(reasons, "受管规则文件内容已变化")
	}
	configContent, configErr := os.ReadFile(a.config.ReloadConfig)
	if configErr != nil {
		return guard.Inspection{}, fmt.Errorf("read Mihomo active config for guard inspection: %w", configErr)
	}
	var document map[string]any
	if err := yaml.Unmarshal(configContent, &document); err != nil {
		return guard.Inspection{}, fmt.Errorf("decode Mihomo active config for guard inspection: %w", err)
	}
	hosts, err := stringMap(document["hosts"])
	if err != nil {
		return guard.Inspection{}, fmt.Errorf("decode Mihomo hosts for guard inspection: %w", err)
	}
	for _, mapping := range normalized.DomainMappings {
		actual, exists := hosts[mapping.Domain]
		if !exists || !hostValueMatches(actual, mapping.Addresses) {
			reasons = append(reasons, "域名映射 "+mapping.Domain+" 已丢失或变化")
		}
	}
	if len(normalized.DomainMappings) > 0 {
		dns, decodeErr := stringMap(document["dns"])
		if decodeErr != nil {
			return guard.Inspection{}, fmt.Errorf("decode Mihomo DNS config for guard inspection: %w", decodeErr)
		}
		if useHosts, ok := dns["use-hosts"].(bool); !ok || !useHosts {
			reasons = append(reasons, "dns.use-hosts 未启用")
		}
	}
	activeRules, err := stringList(document["rules"])
	if err != nil {
		return guard.Inspection{}, fmt.Errorf("decode Mihomo rules for guard inspection: %w", err)
	}
	for _, expected := range expectedRules {
		index := slices.Index(activeRules, expected)
		if index < 0 {
			reasons = append(reasons, "活动配置缺少规则 "+expected)
			continue
		}
		if terminal := firstTerminalRule(activeRules); terminal >= 0 && index > terminal {
			reasons = append(reasons, "规则位于 MATCH/FINAL 之后 "+expected)
		}
	}
	if err := a.verifyOnce(ctx, expectedRules); err != nil {
		reasons = append(reasons, "控制面未加载全部 DIRECT 规则")
	}
	fingerprint := contentHash(configContent)
	if semantic, semanticErr := semanticYAMLHash(configContent); semanticErr == nil {
		fingerprint = semantic
	}
	return guard.Inspection{Healthy: len(reasons) == 0, DriftReasons: reasons, Fingerprint: fingerprint}, nil
}

// VerifyPolicy 不依赖历史收据，验证当前控制面规则和真实 Mihomo 连接。
func (a *Adapter) VerifyPolicy(ctx context.Context, policy proxy.DirectPolicy) error {
	normalized, err := policy.Normalize()
	if err != nil {
		return err
	}
	if err := a.verifyOnce(ctx, rulesForPolicy(normalized)); err != nil {
		return err
	}
	if len(normalized.DomainMappings) > 0 {
		return a.connectionVerifier(ctx, normalized.DomainMappings)
	}
	return nil
}

// VerifyPolicyViaTUN 在仅启用 TUN 时验证当前规则和实际 DIRECT 连接。
func (a *Adapter) VerifyPolicyViaTUN(ctx context.Context, policy proxy.DirectPolicy) error {
	normalized, err := policy.Normalize()
	if err != nil {
		return err
	}
	if err := a.verifyOnce(ctx, rulesForPolicy(normalized)); err != nil {
		return err
	}
	if len(normalized.DomainMappings) > 0 {
		return a.verifyMappedConnectionsViaTUN(ctx, normalized.DomainMappings)
	}
	return nil
}

func providerContainsRules(content []byte, expected []string) bool {
	var document struct {
		Payload []string `yaml:"payload"`
	}
	if yaml.Unmarshal(content, &document) != nil {
		return false
	}
	return slices.Equal(document.Payload, expected)
}

func hostValueMatches(value any, expected []string) bool {
	actual := make([]string, 0, len(expected))
	switch typed := value.(type) {
	case string:
		actual = append(actual, typed)
	case []string:
		actual = append(actual, typed...)
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return false
			}
			actual = append(actual, text)
		}
	default:
		return false
	}
	slices.Sort(actual)
	expected = append([]string(nil), expected...)
	slices.Sort(expected)
	return slices.Equal(actual, expected)
}

func firstTerminalRule(rules []string) int {
	for index, rule := range rules {
		typeName, _, _ := strings.Cut(strings.ToUpper(strings.TrimSpace(rule)), ",")
		if typeName == "MATCH" || typeName == "FINAL" {
			return index
		}
	}
	return -1
}
