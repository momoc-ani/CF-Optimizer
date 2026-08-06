package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/guard"
)

const mihomoGuardID = "mihomo"

// RuleGuardStrategy 使用 Mihomo 控制 API 和活动配置维护上一份已验证策略。
type RuleGuardStrategy struct {
	proxyConfig      config.ProxyConfig
	dataDir          string
	routeBackend     cfnetwork.RouteBackend
	physicalPath     cfnetwork.PhysicalPath
	adapter          *Adapter
	managed          config.MihomoConfig
	rediscoverConfig bool
	tunActive        bool
}

// NewRuleGuardStrategy 创建按内核划分的 Mihomo 规则守护策略。
func NewRuleGuardStrategy(proxyConfig config.ProxyConfig, dataDir string, routeBackend cfnetwork.RouteBackend, physicalPath cfnetwork.PhysicalPath, rediscoverConfig bool) (*RuleGuardStrategy, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("Mihomo rule guard data directory is required")
	}
	return &RuleGuardStrategy{proxyConfig: proxyConfig, dataDir: dataDir, routeBackend: routeBackend, physicalPath: physicalPath, rediscoverConfig: rediscoverConfig}, nil
}

// ID 返回与代理内核一致的稳定策略标识。
func (s *RuleGuardStrategy) ID() string { return mihomoGuardID }

// Observe 发现当前 Mihomo 控制端、活动配置以及系统代理或 TUN 使用状态。
func (s *RuleGuardStrategy) Observe(ctx context.Context) (guard.Observation, error) {
	adapter, managed, detection, err := s.resolveAdapter(ctx)
	if err != nil {
		return guard.Observation{}, err
	}
	if adapter == nil || !detection.Present {
		message := detection.Message
		if message == "" {
			message = "未发现正在运行的 Mihomo 内核"
		}
		return guard.Observation{Activity: guard.ActivityOffline, Message: message}, nil
	}
	activity, err := adapter.runtimeActivity(ctx)
	if err != nil {
		return guard.Observation{}, err
	}
	proxyState, proxyMessage := platformSystemProxyState(ctx, activity.Ports)
	observation := guard.Observation{
		Online: true, Manageable: managed.ReloadConfig != "", Endpoint: detection.Endpoint,
		ConfigPath: managed.ReloadConfig, TUNActive: activity.TUN,
	}
	if activity.TUN || proxyState == systemProxyOn {
		observation.Activity = guard.ActivityActive
		observation.SystemProxyActive = proxyState == systemProxyOn
		observation.Message = "Mihomo 正在承载系统代理流量"
	} else if proxyState == systemProxyUnknown {
		observation.Activity = guard.ActivityUnknown
		observation.Message = proxyMessage
	} else {
		observation.Activity = guard.ActivityInactive
		observation.Message = proxyMessage
	}
	observation.Revision = detection.Endpoint + "\x00" + managed.ReloadConfig
	if content, readErr := os.ReadFile(managed.ReloadConfig); readErr == nil {
		observation.Revision += "\x00" + contentHash(content)
	}
	s.adapter = adapter
	s.managed = managed
	s.tunActive = activity.TUN
	return observation, nil
}

// Inspect 比较活动配置、控制面规则和上一份已验证策略。
func (s *RuleGuardStrategy) Inspect(ctx context.Context, desired guard.DesiredPolicy) (guard.Inspection, error) {
	if s.adapter == nil {
		return guard.Inspection{}, errors.New("Mihomo adapter is unavailable for inspection")
	}
	return s.adapter.InspectManagedPolicy(ctx, desired.Policy)
}

// Plan 使用现有 Mihomo 事务计划器生成幂等修复计划。
func (s *RuleGuardStrategy) Plan(ctx context.Context, desired guard.DesiredPolicy, _ guard.Inspection) (guard.RepairPlan, error) {
	if s.adapter == nil {
		return guard.RepairPlan{}, errors.New("Mihomo adapter is unavailable for planning")
	}
	plan, err := s.adapter.Plan(ctx, desired.Policy)
	if err != nil {
		return guard.RepairPlan{}, err
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		return guard.RepairPlan{}, err
	}
	return guard.RepairPlan{ID: plan.ID, Target: s.ID(), Summary: append([]string(nil), plan.Summary...), Payload: payload}, nil
}

// Apply 原子应用 Mihomo 修复计划，并保留本次失败回滚收据。
func (s *RuleGuardStrategy) Apply(ctx context.Context, plan guard.RepairPlan) (guard.RepairReceipt, error) {
	if s.adapter == nil {
		return guard.RepairReceipt{}, errors.New("Mihomo adapter is unavailable for apply")
	}
	var proxyPlan proxy.Plan
	if err := json.Unmarshal(plan.Payload, &proxyPlan); err != nil {
		return guard.RepairReceipt{}, fmt.Errorf("decode Mihomo guard plan: %w", err)
	}
	receipt, err := s.adapter.Apply(ctx, proxyPlan)
	if err != nil {
		return guard.RepairReceipt{}, err
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		if receipt.Changed {
			_ = s.adapter.Rollback(context.WithoutCancel(ctx), receipt)
		}
		return guard.RepairReceipt{}, err
	}
	return guard.RepairReceipt{ID: receipt.ID, Target: s.ID(), Changed: receipt.Changed, AppliedAt: receipt.AppliedAt, Payload: payload}, nil
}

// Verify 严格核对活动规则、Mihomo DIRECT 连接和物理出口路由。
func (s *RuleGuardStrategy) Verify(ctx context.Context, desired guard.DesiredPolicy, receipt guard.RepairReceipt) (guard.Verification, error) {
	if s.adapter == nil {
		return guard.Verification{RollbackRecommended: receipt.Changed}, errors.New("Mihomo adapter is unavailable for verification")
	}
	inspection, err := s.adapter.InspectManagedPolicy(ctx, desired.Policy)
	if err != nil {
		return guard.Verification{FailureKind: "inspection_failed", RollbackRecommended: receipt.Changed}, err
	}
	if !inspection.Healthy {
		return guard.Verification{FailureKind: "rules_not_active", RollbackRecommended: receipt.Changed}, fmt.Errorf("Mihomo rules remain drifted: %s", strings.Join(inspection.DriftReasons, "; "))
	}
	verifyErr := s.adapter.VerifyPolicy(ctx, desired.Policy)
	if errors.Is(verifyErr, errMixedPortUnavailable) && s.tunActive && len(desired.Policy.DomainMappings) > 0 {
		verifyErr = s.adapter.VerifyPolicyViaTUN(ctx, desired.Policy)
	}
	if verifyErr != nil {
		verification := guard.Verification{FailureKind: "connection_failed", RollbackRecommended: receipt.Changed && !transientGuardVerification(verifyErr)}
		return verification, verifyErr
	}
	verification := guard.Verification{Verified: true, Direct: len(desired.Policy.DomainMappings) > 0, Message: "Mihomo 规则已加载"}
	if len(desired.Policy.DomainMappings) > 0 && s.routeBackend == nil {
		return guard.Verification{FailureKind: "physical_route_unavailable"}, errors.New("physical route backend is unavailable for Mihomo guard verification")
	}
	for _, mapping := range desired.Policy.DomainMappings {
		for _, rawAddress := range mapping.Addresses {
			address, parseErr := netip.ParseAddr(rawAddress)
			if parseErr != nil {
				return guard.Verification{FailureKind: "invalid_target", RollbackRecommended: receipt.Changed}, parseErr
			}
			resolved, resolveErr := s.routeBackend.Resolve(ctx, address.Unmap())
			if resolveErr != nil {
				return guard.Verification{FailureKind: "physical_route_unavailable"}, fmt.Errorf("resolve physical route for %s: %w", address, resolveErr)
			}
			if err := verifyPhysicalRoute(resolved, s.physicalPath, address.Is4()); err != nil {
				return guard.Verification{FailureKind: "physical_route_mismatch"}, err
			}
			verification.TargetAddresses = append(verification.TargetAddresses, address.Unmap().String())
			verification.Interface = resolved.Interface
			verification.Gateway = resolved.Gateway
		}
	}
	if verification.Direct {
		verification.Message = "Mihomo DIRECT、目标地址和物理出口已验证"
	}
	return verification, nil
}

// Rollback 仅撤销本次未通过验证的 Mihomo 修复，不触碰持久化优选结果。
func (s *RuleGuardStrategy) Rollback(ctx context.Context, receipt guard.RepairReceipt) error {
	if !receipt.Changed {
		return nil
	}
	if s.adapter == nil {
		return errors.New("Mihomo adapter is unavailable for rollback")
	}
	var proxyReceipt proxy.Receipt
	if err := json.Unmarshal(receipt.Payload, &proxyReceipt); err != nil {
		return fmt.Errorf("decode Mihomo guard receipt: %w", err)
	}
	return s.adapter.Rollback(ctx, proxyReceipt)
}

func (s *RuleGuardStrategy) resolveAdapter(ctx context.Context) (*Adapter, config.MihomoConfig, proxy.Detection, error) {
	configured := s.proxyConfig.Mihomo
	discoveryConfig := configured
	if s.rediscoverConfig {
		discoveryConfig.ReloadConfig = ""
	}
	if s.adapter != nil {
		detection, err := s.adapter.Detect(ctx)
		if err == nil && detection.Present {
			detection.Endpoint = s.managed.Controller
			managed := s.managed
			if s.proxyConfig.AutoDetect {
				if refreshed, refreshErr := ConfigureDetected(discoveryConfig, detection, s.dataDir); refreshErr == nil {
					managed = refreshed
				}
			}
			if managed.Controller != s.managed.Controller || managed.ReloadConfig != s.managed.ReloadConfig {
				adapter, newErr := New(managed)
				if newErr != nil {
					return nil, config.MihomoConfig{}, proxy.Detection{}, newErr
				}
				s.adapter = adapter
				s.managed = managed
			}
			detection.ConfigPath = managed.ReloadConfig
			return s.adapter, managed, detection, nil
		}
	}
	if s.proxyConfig.AutoDetect {
		detection, err := AutoDetect(ctx, discoveryConfig)
		if err != nil && !detection.Present {
			return nil, config.MihomoConfig{}, detection, nil
		}
		if !detection.Present {
			return nil, config.MihomoConfig{}, detection, nil
		}
		managed, configureErr := ConfigureDetected(discoveryConfig, detection, s.dataDir)
		if configureErr != nil {
			detection.Message = configureErr.Error()
			probeConfig := discoveryConfig
			probeConfig.Controller = detection.Endpoint
			adapter, newErr := New(probeConfig)
			if newErr != nil {
				return nil, config.MihomoConfig{}, proxy.Detection{}, newErr
			}
			return adapter, probeConfig, detection, nil
		}
		adapter, newErr := New(managed)
		if newErr != nil {
			return nil, config.MihomoConfig{}, proxy.Detection{}, newErr
		}
		detection.ConfigPath = managed.ReloadConfig
		s.rediscoverConfig = true
		return adapter, managed, detection, nil
	}
	if !configured.Enabled {
		return nil, config.MihomoConfig{}, proxy.Detection{Message: "Mihomo 适配器未启用"}, nil
	}
	adapter, err := New(configured)
	if err != nil {
		return nil, config.MihomoConfig{}, proxy.Detection{}, err
	}
	detection, err := adapter.Detect(ctx)
	if err != nil {
		return nil, config.MihomoConfig{}, proxy.Detection{Message: err.Error()}, nil
	}
	return adapter, configured, detection, nil
}

func verifyPhysicalRoute(resolved cfnetwork.ResolvedRoute, physicalPath cfnetwork.PhysicalPath, ipv4 bool) error {
	if resolved.Interface != physicalPath.Interface && (physicalPath.InterfaceIndex <= 0 || resolved.InterfaceIndex != physicalPath.InterfaceIndex) {
		return fmt.Errorf("route uses interface %q instead of %q", resolved.Interface, physicalPath.Interface)
	}
	expectedGateway := physicalPath.GatewayIPv6
	if ipv4 {
		expectedGateway = physicalPath.GatewayIPv4
	}
	if expectedGateway != "" && resolved.Gateway != expectedGateway {
		return fmt.Errorf("route uses gateway %q instead of %q", resolved.Gateway, expectedGateway)
	}
	return nil
}

func transientGuardVerification(err error) bool {
	var verificationErr *proxy.DomainVerificationError
	if errors.As(err, &verificationErr) {
		return verificationErr.Kind == proxy.DomainVerificationCandidateUnreachable || verificationErr.Kind == proxy.DomainVerificationMappingNotPropagated
	}
	return errors.Is(err, context.DeadlineExceeded)
}

var _ guard.Strategy = (*RuleGuardStrategy)(nil)
