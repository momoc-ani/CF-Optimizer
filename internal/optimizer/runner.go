package optimizer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/acceleration"
	"github.com/cf-optimizer/cf-optimizer/internal/benchmark"
	"github.com/cf-optimizer/cf-optimizer/internal/candidates"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/ranges"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

// ErrAlreadyRunning 表示已有测速任务持有进程内单任务锁。
var ErrAlreadyRunning = errors.New("an optimization run is already active")

const (
	managedRouteMetric       = 5
	routeCleanupTimeoutFloor = 30 * time.Second
	domainEvidenceLifetime   = 15 * time.Minute
)

const (
	stagePoolRefresh = "pool_refresh"
	stagePoolReuse   = "pool_reuse"
	stageDomain      = "domain_qualify"
	stagePolicyPlan  = "policy_plan"
	stageApplyVerify = "apply_verify"
	stageCommit      = "commit"
)

// RangeSource 为运行器提供不可变网段快照，便于测试替换远程来源。
type RangeSource interface {
	Update(context.Context, bool) (ranges.UpdateResult, error)
}

// RangeSnapshotLoader 在官方网段刷新异常时提供本地最后有效快照。
type RangeSnapshotLoader interface {
	Load() (ranges.Snapshot, error)
}

// Benchmarker 隔离两阶段测速实现。
type Benchmarker interface {
	Run(context.Context, []netip.Addr, func(benchmark.Progress)) ([]benchmark.Result, error)
}

// NodePoolValidator 复用有效节点池时只验证当前 TCP/TLS 物理路径，不重新下载测速。
type NodePoolValidator interface {
	Validate(context.Context, []benchmark.Result, func(benchmark.Progress)) ([]benchmark.Result, error)
}

// physicalBenchmarkPathVerifier 通过真实绑定 Socket 提供物理路由回退证据。
type physicalBenchmarkPathVerifier interface {
	VerifyPhysicalPath(context.Context, []netip.Addr) (proxy.BenchmarkPathEvidence, error)
}

// NetworkFingerprinter 返回当前默认路径和活动接口摘要，用于使跨网络节点池失效。
type NetworkFingerprinter func(context.Context, time.Duration) (string, error)

// PolicyApplier 隔离代理策略协调器。
type PolicyApplier interface {
	Capabilities() proxy.Capabilities
	Apply(context.Context, proxy.DirectPolicy) (proxy.ApplyResult, error)
	Rollback(context.Context, proxy.ApplyResult) error
}

// activeCapabilitiesProvider 为可动态发现适配器的策略协调器提供当前可用能力。
type activeCapabilitiesProvider interface {
	ActiveCapabilities(context.Context) proxy.Capabilities
}

const (
	// ManualMappingApplyStateApplied 表示映射已应用并通过验证。
	ManualMappingApplyStateApplied = "applied"
	// ManualMappingApplyStatePartial 表示部分映射能力已应用，仍有能力等待恢复。
	ManualMappingApplyStatePartial = "partial"
	// ManualMappingApplyStateDeferred 表示映射已保存，但当前没有可用适配器。
	ManualMappingApplyStateDeferred = "deferred"
	// domainMappingCapabilityUnavailableReason 是域名分配阶段可安全延后应用的稳定原因。
	domainMappingCapabilityUnavailableReason = "domain mapping capability is unavailable"
)

// ManualMappingApplyResult 汇总手动域名映射的持久化、应用和能力降级状态。
type ManualMappingApplyResult struct {
	PolicyRefreshed     bool
	ApplyState          string
	AppliedAdapters     []string
	SkippedCapabilities []string
	Warnings            []string
}

type policyReceiptJournalSetter interface {
	SetReceiptJournal(proxy.ReceiptJournal)
}

type managedPolicyCleaner interface {
	Cleanup(context.Context, proxy.ApplyResult) error
}

// DomainMappingVerifier 验证映射应用前后的 HTTPS 连接证据。
type DomainMappingVerifier interface {
	VerifyPreflight(context.Context, []proxy.DomainMapping) error
	VerifyApplied(context.Context, []proxy.DomainMapping) error
}

// DomainResolver 通过物理网络返回域名的真实地址，用于 Cloudflare 归属校验。
type DomainResolver interface {
	Resolve(context.Context, string) ([]netip.Addr, error)
}

// DomainDownloadTester 使用目标域名自己的资源复测精确候选地址的直连下载速度。
type DomainDownloadTester interface {
	DiscoverProbeURL(context.Context, string, string) (string, error)
	Measure(context.Context, string, string, string) (acceleration.DownloadResult, error)
}

// Event 是后台任务发送给 IPC 和桌面界面的版本稳定进度载荷。
type Event struct {
	RunID     string              `json:"run_id"`
	Type      string              `json:"type"`
	Stage     string              `json:"stage,omitempty"`
	Message   string              `json:"message,omitempty"`
	Progress  *benchmark.Progress `json:"progress,omitempty"`
	Timestamp time.Time           `json:"timestamp"`
}

// RunOptions 区分只读测速与会应用路由/代理策略的完整优选。
type RunOptions struct {
	ForceRangeRefresh bool `json:"force_range_refresh"`
	ApplyPolicy       bool `json:"apply_policy"`
}

// DiscoveredDomainCleanupResult 汇总自动发现记录和既有加速映射的清理结果。
type DiscoveredDomainCleanupResult struct {
	Cleared              int  `json:"cleared"`
	AccelerationsRemoved int  `json:"accelerations_removed"`
	PolicyRefreshed      bool `json:"policy_refreshed"`
}

// DomainAllocationResult 记录单个加速域名从物理 DNS 校验到优选地址分配的结果。
type DomainAllocationResult struct {
	Domain             string   `json:"domain"`
	Source             string   `json:"source"`
	ResolvedAddresses  []string `json:"resolved_addresses,omitempty"`
	AssignedAddress    string   `json:"assigned_address,omitempty"`
	DownloadAddress    string   `json:"download_address,omitempty"`
	DownloadProbeURL   string   `json:"download_probe_url,omitempty"`
	CloudflareVerified bool     `json:"cloudflare_verified"`
	PreflightVerified  bool     `json:"preflight_verified"`
	DownloadVerified   bool     `json:"download_verified"`
	DownloadMbps       float64  `json:"download_mbps,omitempty"`
	Error              string   `json:"error,omitempty"`
}

type domainAllocationCandidate struct {
	domain string
	source string
}

// domainAllocationOptions 区分完整优选的排名池与维护流程必须保留的显式映射。
type domainAllocationOptions struct {
	manualMappingOverrides map[string]string
	excludedAddresses      map[string]map[string]struct{}
}

// RunReport 汇总候选结果、地址族决策、策略状态和可恢复警告。
type RunReport struct {
	ID                        string                        `json:"id"`
	StartedAt                 time.Time                     `json:"started_at"`
	FinishedAt                time.Time                     `json:"finished_at"`
	RangeSource               string                        `json:"range_source"`
	RangeHash                 string                        `json:"range_hash"`
	Results                   []benchmark.Result            `json:"results"`
	IPv4Decision              Decision                      `json:"ipv4_decision"`
	IPv6Decision              Decision                      `json:"ipv6_decision"`
	PolicyApplied             bool                          `json:"policy_applied"`
	BenchmarkPath             []proxy.BenchmarkPathEvidence `json:"benchmark_path,omitempty"`
	DomainAllocations         []DomainAllocationResult      `json:"domain_allocations,omitempty"`
	Warnings                  []string                      `json:"warnings,omitempty"`
	NodePoolID                string                        `json:"node_pool_id,omitempty"`
	NodePoolState             string                        `json:"node_pool_state,omitempty"`
	NodePoolValidUntil        time.Time                     `json:"node_pool_valid_until,omitempty"`
	domainMappings            []proxy.DomainMapping
	domainAllocationCompleted bool
}

// Runner 协调网段、候选、测速、稳定选择、路由和代理策略的完整事务。
type Runner struct {
	config                   config.Config
	ranges                   RangeSource
	benchmark                Benchmarker
	store                    *store.Store
	routes                   *cfnetwork.RouteController
	physicalPath             cfnetwork.PhysicalPath
	policy                   PolicyApplier
	domainVerifier           DomainMappingVerifier
	domainResolver           DomainResolver
	domainDownload           DomainDownloadTester
	networkFingerprint       NetworkFingerprinter
	activeNetworkFingerprint string
	logger                   *slog.Logger
	now                      func() time.Time
	runMutex                 sync.Mutex
	pendingRuns              atomic.Int32
	operationGate            operationGate
}

// SetDomainMappingVerifier 注入生产环境的物理接口 HTTPS 验证器。
func (r *Runner) SetDomainMappingVerifier(verifier DomainMappingVerifier) {
	r.domainVerifier = verifier
}

// SetDomainResolver 注入绕过代理 Fake-IP 的物理 DNS 解析器。
func (r *Runner) SetDomainResolver(resolver DomainResolver) {
	r.domainResolver = resolver
}

// SetDomainDownloadTester 注入绑定物理接口的手动域名下载复测器。
func (r *Runner) SetDomainDownloadTester(tester DomainDownloadTester) {
	r.domainDownload = tester
}

// SetNetworkFingerprinter 注入当前操作系统网络路径指纹读取器。
func (r *Runner) SetNetworkFingerprinter(fingerprinter NetworkFingerprinter) {
	r.networkFingerprint = fingerprinter
}

// TestManualDomain 使用临时物理路由完成单个手动域名的 SNI、Host 和同域下载复测。
func (r *Runner) TestManualDomain(ctx context.Context, domain, rawAddress string) (acceleration.DownloadResult, error) {
	if r.domainVerifier == nil || r.domainDownload == nil {
		return acceleration.DownloadResult{}, errors.New("manual domain verification is unavailable")
	}
	var result acceleration.DownloadResult
	err := r.TryPolicyMaintenance(ctx, func(ctx context.Context) error {
		transactionID, err := r.applyDomainProbeRoute(ctx, rawAddress)
		if err != nil {
			return fmt.Errorf("prepare direct download route for %s via %s: %w", domain, rawAddress, err)
		}
		if transactionID != "" {
			defer func() {
				if rollbackErr := r.rollbackRoutes(ctx, []string{transactionID}); rollbackErr != nil {
					r.logger.Error("单域名复测临时路由清理失败", "domain", domain, "target_ip", rawAddress, "error", rollbackErr)
				}
			}()
		}
		mapping := proxy.DomainMapping{Domain: domain, Addresses: []string{rawAddress}}
		if err := r.domainVerifier.VerifyPreflight(ctx, []proxy.DomainMapping{mapping}); err != nil {
			return fmt.Errorf("verify manual domain %s via %s: %w", domain, rawAddress, err)
		}
		probeURL, err := r.domainDownload.DiscoverProbeURL(ctx, domain, rawAddress)
		if err != nil {
			return fmt.Errorf("discover manual domain download resource %s via %s: %w", domain, rawAddress, err)
		}
		result, err = r.domainDownload.Measure(ctx, domain, rawAddress, probeURL)
		if err != nil {
			return fmt.Errorf("measure manual domain download %s via %s: %w", domain, rawAddress, err)
		}
		return nil
	})
	return result, err
}

// ApplyManualDomainMapping 保留旧版 bool 返回契约，并转发到结构化手动映射流程。
func (r *Runner) ApplyManualDomainMapping(ctx context.Context, domain, rawAddress string, mappings map[string]string) (bool, error) {
	result, err := r.ApplyManualDomainMappingDetailed(ctx, domain, rawAddress, mappings)
	return result.PolicyRefreshed, err
}

// ApplyManualDomainMappingDetailed 保存映射；适配器不可用时返回 deferred 而不回滚配置。
func (r *Runner) ApplyManualDomainMappingDetailed(ctx context.Context, domain, rawAddress string, mappings map[string]string) (ManualMappingApplyResult, error) {
	result := ManualMappingApplyResult{ApplyState: ManualMappingApplyStateDeferred}
	if !r.tryAcquireMaintenance() {
		return result, ErrAlreadyRunning
	}
	defer r.operationGate.release()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	previousMappings := cloneManualMappings(r.config.Acceleration.ManualMappings)
	nextMappings := cloneManualMappings(mappings)
	normalizedDomain := normalizeDomainForMapping(domain)
	nextMappings[normalizedDomain] = strings.TrimSpace(rawAddress)
	stateBefore := r.store.Snapshot()
	r.config.Acceleration.ManualMappings = nextMappings

	capabilities := r.activePolicyCapabilities(ctx)
	if r.policy == nil || !capabilities.DomainMappings {
		result.SkippedCapabilities = []string{"domain_mappings"}
		result.Warnings = []string{"当前没有可用的域名映射适配器；映射已保存，请在适配器恢复后重新应用或刷新策略"}
		r.logger.Warn("手动域名映射已保存但暂未应用", "domain", normalizedDomain, "policy_refreshed", false, "result", "deferred")
		return result, nil
	}

	var applyErr error
	if stateBefore.Policy == nil || !storedPolicyCoveredByCapabilities(stateBefore.Policy, capabilities) {
		applyResult, _, err := r.applyManualMappingOnlyLocked(ctx, stateBefore, nextMappings, capabilities)
		if err == nil {
			result = manualMappingResultFromApply(applyResult, false)
			result.PolicyRefreshed = true
			return result, nil
		}
		applyErr = err
	} else {
		applyErr = r.refreshPolicyWithManualMappingsLocked(ctx, nextMappings, nil)
		if applyErr == nil {
			result.ApplyState = ManualMappingApplyStateApplied
			result.PolicyRefreshed = true
			r.logger.Info("手动域名映射已应用并验证", "domain", normalizedDomain, "target_ip", strings.TrimSpace(rawAddress), "policy_refreshed", true, "result", "completed")
			return result, nil
		}
	}
	// 仅应用前的能力缺失可降级；验证或回滚错误必须向上返回，不能被离线复检吞掉。
	if errors.Is(applyErr, proxy.ErrNoActiveAdapters) || errors.Is(applyErr, proxy.ErrDomainMappingsUnavailable) {
		result.SkippedCapabilities = []string{"domain_mappings"}
		result.Warnings = []string{"域名映射适配器在应用期间不可用；映射已保存，请在适配器恢复后重新应用或刷新策略"}
		r.logger.Warn("手动域名映射应用延后", "domain", normalizedDomain, "result", "deferred", "error", applyErr)
		return result, nil
	}
	r.config.Acceleration.ManualMappings = previousMappings
	return result, fmt.Errorf("refresh policy after manual domain mapping: %w", applyErr)
}

// activePolicyCapabilities 返回当前已检测可用能力，兼容旧的静态策略替身。
func (r *Runner) activePolicyCapabilities(ctx context.Context) proxy.Capabilities {
	if r.policy == nil {
		return proxy.Capabilities{}
	}
	if provider, ok := r.policy.(activeCapabilitiesProvider); ok {
		return provider.ActiveCapabilities(ctx)
	}
	return r.policy.Capabilities()
}

// storedPolicyCoveredByCapabilities 判断完整旧策略能否由当前活动能力继续维护。
func storedPolicyCoveredByCapabilities(policy *store.PolicySnapshot, capabilities proxy.Capabilities) bool {
	if policy == nil {
		return false
	}
	if len(policy.IPv4CIDRs) > 0 && !capabilities.IPv4 {
		return false
	}
	if len(policy.IPv6CIDRs) > 0 && !capabilities.IPv6 {
		return false
	}
	if len(policy.Domains) > 0 && !capabilities.Domains {
		return false
	}
	return len(policy.DomainMappings) == 0 || capabilities.DomainMappings
}

// applyManualMappingOnlyLocked 只向映射适配器提交手动域名，避免覆盖离线适配器的旧 IP 策略。
func (r *Runner) applyManualMappingOnlyLocked(ctx context.Context, before store.State, mappings map[string]string, capabilities proxy.Capabilities) (proxy.ApplyResult, proxy.DirectPolicy, error) {
	if r.policy == nil {
		return proxy.ApplyResult{}, proxy.DirectPolicy{}, errors.New("policy application requested but no adapter is configured")
	}
	policy := proxy.DirectPolicy{}
	for _, domain := range r.config.Acceleration.ManualDomains {
		normalized := normalizeDomainForMapping(domain)
		address := strings.TrimSpace(mappings[normalized])
		if address == "" {
			continue
		}
		policy.DomainMappings = append(policy.DomainMappings, proxy.DomainMapping{Domain: normalized, Addresses: []string{address}})
	}
	if capabilities.Domains {
		for _, mapping := range policy.DomainMappings {
			policy.Domains = append(policy.Domains, mapping.Domain)
		}
	}
	normalized, err := policy.Normalize()
	if err != nil {
		return proxy.ApplyResult{}, proxy.DirectPolicy{}, err
	}
	applied, err := r.policy.Apply(ctx, normalized)
	if err != nil {
		return proxy.ApplyResult{}, proxy.DirectPolicy{}, err
	}
	if len(normalized.DomainMappings) > 0 {
		if r.domainVerifier == nil {
			if rollbackErr := r.policy.Rollback(ctx, applied); rollbackErr != nil {
				return proxy.ApplyResult{}, proxy.DirectPolicy{}, errors.Join(errors.New("domain mapping verification is unavailable"), rollbackErr)
			}
			return proxy.ApplyResult{}, proxy.DirectPolicy{}, errors.New("domain mapping verification is unavailable")
		}
		if err := r.domainVerifier.VerifyApplied(ctx, normalized.DomainMappings); err != nil {
			if rollbackErr := r.policy.Rollback(ctx, applied); rollbackErr != nil {
				err = errors.Join(err, rollbackErr)
			}
			return proxy.ApplyResult{}, proxy.DirectPolicy{}, err
		}
	}
	mergedPolicy := mergeManualMappingsIntoStoredPolicy(before.Policy, normalized)
	committed := applied
	if before.Policy != nil {
		var previous proxy.ApplyResult
		if err := json.Unmarshal(before.Policy.Receipts, &previous); err != nil {
			_ = r.policy.Rollback(ctx, applied)
			return proxy.ApplyResult{}, proxy.DirectPolicy{}, fmt.Errorf("decode previous policy receipts: %w", err)
		}
		committed.Receipts = append(previous.Receipts, applied.Receipts...)
	}
	receipts, err := json.Marshal(committed)
	if err != nil {
		_ = r.policy.Rollback(ctx, applied)
		return proxy.ApplyResult{}, proxy.DirectPolicy{}, err
	}
	if err := r.store.Update(func(state *store.State) error {
		state.Policy = policySnapshot(mergedPolicy, receipts, r.now().UTC())
		for _, mapping := range normalized.DomainMappings {
			record := state.DiscoveredDomains[mapping.Domain]
			record.Domain = mapping.Domain
			record.Source = "manual"
			record.Active = true
			record.LastError = ""
			state.DiscoveredDomains[mapping.Domain] = record
		}
		return nil
	}); err != nil {
		_ = r.policy.Rollback(ctx, applied)
		return proxy.ApplyResult{}, proxy.DirectPolicy{}, err
	}
	return applied, normalized, nil
}

// mergeManualMappingsIntoStoredPolicy 将新手动映射合并到旧策略快照，保留自动域名和地址族规则。
func mergeManualMappingsIntoStoredPolicy(previous *store.PolicySnapshot, mappingPolicy proxy.DirectPolicy) proxy.DirectPolicy {
	if previous == nil {
		return mappingPolicy
	}
	merged := proxy.DirectPolicy{
		IPv4CIDRs: append([]string(nil), previous.IPv4CIDRs...), IPv6CIDRs: append([]string(nil), previous.IPv6CIDRs...),
		Domains: append([]string(nil), previous.Domains...), Processes: append([]string(nil), previous.Processes...),
	}
	manual := make(map[string]proxy.DomainMapping, len(mappingPolicy.DomainMappings))
	for _, mapping := range mappingPolicy.DomainMappings {
		manual[normalizeDomainForMapping(mapping.Domain)] = mapping
	}
	for _, snapshot := range previous.DomainMappings {
		domain := normalizeDomainForMapping(snapshot.Domain)
		if replacement, exists := manual[domain]; exists {
			merged.DomainMappings = append(merged.DomainMappings, replacement)
			delete(manual, domain)
			continue
		}
		merged.DomainMappings = append(merged.DomainMappings, proxy.DomainMapping{Domain: domain, Addresses: append([]string(nil), snapshot.Addresses...)})
	}
	for _, mapping := range manual {
		merged.DomainMappings = append(merged.DomainMappings, mapping)
	}
	merged, _ = merged.Normalize()
	return merged
}

// manualMappingResultFromApply 将协调器收据转换为稳定的前端状态载荷。
func manualMappingResultFromApply(applied proxy.ApplyResult, partial bool) ManualMappingApplyResult {
	result := ManualMappingApplyResult{PolicyRefreshed: len(applied.Receipts) > 0, ApplyState: ManualMappingApplyStateDeferred}
	seen := make(map[string]struct{}, len(applied.Receipts))
	for _, receipt := range applied.Receipts {
		if _, exists := seen[receipt.Adapter]; exists {
			continue
		}
		seen[receipt.Adapter] = struct{}{}
		result.AppliedAdapters = append(result.AppliedAdapters, receipt.Adapter)
	}
	if len(result.AppliedAdapters) > 0 {
		result.ApplyState = ManualMappingApplyStateApplied
		if partial {
			result.ApplyState = ManualMappingApplyStatePartial
		}
	}
	return result
}

func cloneManualMappings(mappings map[string]string) map[string]string {
	clone := make(map[string]string, len(mappings))
	for domain, address := range mappings {
		clone[normalizeDomainForMapping(domain)] = strings.TrimSpace(address)
	}
	return clone
}

func normalizeDomainForMapping(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

// NewRunner 创建依赖显式注入的优选运行器。
func NewRunner(cfg config.Config, rangeSource RangeSource, benchmarker Benchmarker, stateStore *store.Store, routes *cfnetwork.RouteController, physicalPath cfnetwork.PhysicalPath, policy PolicyApplier, logger *slog.Logger) (*Runner, error) {
	if rangeSource == nil || benchmarker == nil || stateStore == nil || logger == nil {
		return nil, errors.New("range source, benchmarker, store and logger are required")
	}
	policy = normalizePolicyApplier(policy)
	configurePolicyJournal(policy, stateStore)
	return &Runner{
		config: cfg, ranges: rangeSource, benchmark: benchmarker, store: stateStore,
		routes: routes, physicalPath: physicalPath, policy: policy,
		logger: logger.With("component", "optimizer"), now: time.Now,
	}, nil
}

// Reconfigure 在无运行任务时原子替换 Runner 的运行依赖，并可刷新当前策略。
func (r *Runner) Reconfigure(
	ctx context.Context,
	cfg config.Config,
	rangeSource RangeSource,
	benchmarker Benchmarker,
	routes *cfnetwork.RouteController,
	physicalPath cfnetwork.PhysicalPath,
	policy PolicyApplier,
	domainResolver DomainResolver,
	domainVerifier DomainMappingVerifier,
	domainDownload DomainDownloadTester,
	refreshPolicy bool,
) (bool, error) {
	if rangeSource == nil || benchmarker == nil {
		return false, errors.New("range source and benchmarker are required for runtime reload")
	}
	if !r.tryAcquireMaintenance() {
		return false, ErrAlreadyRunning
	}
	defer r.operationGate.release()

	policy = normalizePolicyApplier(policy)
	configurePolicyJournal(policy, r.store)
	previousConfig, previousRanges, previousBenchmark := r.config, r.ranges, r.benchmark
	previousRoutes, previousPath, previousPolicy := r.routes, r.physicalPath, r.policy
	previousResolver, previousVerifier, previousDownload := r.domainResolver, r.domainVerifier, r.domainDownload
	r.config, r.ranges, r.benchmark = cfg, rangeSource, benchmarker
	r.routes, r.physicalPath, r.policy = routes, physicalPath, policy
	r.domainResolver, r.domainVerifier, r.domainDownload = domainResolver, domainVerifier, domainDownload
	r.activeNetworkFingerprint = ""

	rollback := func() {
		r.config, r.ranges, r.benchmark = previousConfig, previousRanges, previousBenchmark
		r.routes, r.physicalPath, r.policy = previousRoutes, previousPath, previousPolicy
		r.domainResolver, r.domainVerifier, r.domainDownload = previousResolver, previousVerifier, previousDownload
		r.activeNetworkFingerprint = ""
	}
	if refreshPolicy && r.store.Snapshot().Policy != nil {
		if r.policy == nil {
			r.logger.Warn("运行配置已热重载但当前策略无法刷新", "policy_snapshot_present", true, "result", "deferred")
			return false, nil
		}
		if err := r.refreshPolicyLocked(ctx); err != nil {
			rollback()
			return false, fmt.Errorf("refresh policy after runtime reload: %w", err)
		}
		r.logger.Info("运行配置与当前策略已热重载", "policy_refreshed", true, "result", "completed")
		return true, nil
	}
	r.logger.Info("运行配置已热重载", "policy_refreshed", false, "result", "completed")
	return false, nil
}

// normalizePolicyApplier 将带 nil 具体指针的接口归一化为 nil。
func normalizePolicyApplier(policy PolicyApplier) PolicyApplier {
	// Go 接口可能携带 nil 具体指针；在构造边界归一化，避免无适配器配置被误判为可应用策略。
	if policy == nil {
		return nil
	}
	policyValue := reflect.ValueOf(policy)
	if policyValue.Kind() == reflect.Pointer && policyValue.IsNil() {
		return nil
	}
	return policy
}

// configurePolicyJournal 为支持事务日志的策略适配器注入持久化边界。
func configurePolicyJournal(policy PolicyApplier, stateStore *store.Store) {
	if journaledPolicy, ok := policy.(policyReceiptJournalSetter); ok {
		journaledPolicy.SetReceiptJournal(newPolicyReceiptJournal(stateStore))
	}
}

// Run 执行一次可取消优选；同一 Runner 同时只允许一个任务。
func (r *Runner) Run(ctx context.Context, options RunOptions, emit func(Event)) (report RunReport, runErr error) {
	r.pendingRuns.Add(1)
	if !r.runMutex.TryLock() {
		r.pendingRuns.Add(-1)
		return RunReport{}, ErrAlreadyRunning
	}
	defer func() {
		r.runMutex.Unlock()
		r.pendingRuns.Add(-1)
	}()
	if err := r.operationGate.acquire(ctx); err != nil {
		return RunReport{}, err
	}
	defer r.operationGate.release()
	report.ID = newRunID()
	report.StartedAt = r.now().UTC()
	currentStage := stagePoolRefresh
	r.emit(emit, Event{RunID: report.ID, Type: "run.started", Stage: "ranges", Message: "optimization started"})
	if err := r.store.Update(func(state *store.State) error {
		state.Running = true
		state.LastStartedAt = report.StartedAt
		state.LastError = ""
		return nil
	}); err != nil {
		return report, err
	}
	defer func() {
		report.FinishedAt = r.now().UTC()
		if runErr != nil {
			if checkpointErr := r.markCheckpointFailure(report.ID, currentStage, runErr); checkpointErr != nil {
				runErr = errors.Join(runErr, checkpointErr)
			}
		}
		if finalizeErr := r.finalize(report, runErr); finalizeErr != nil {
			runErr = errors.Join(runErr, finalizeErr)
		}
		result := "completed"
		if errors.Is(runErr, context.Canceled) {
			result = "cancelled"
		} else if runErr != nil {
			result = "failed"
		} else if manualDomainAllocationFailure(report) != "" {
			result = "partial"
		}
		r.logger.Info("优选任务结束", "run_id", report.ID, "duration", report.FinishedAt.Sub(report.StartedAt), "result", result, "error", runErr)
		r.emit(emit, Event{RunID: report.ID, Type: "run.finished", Stage: "complete", Message: result})
	}()
	r.logger.Info("优选任务开始", "run_id", report.ID, "apply_policy", options.ApplyPolicy)

	rangeResult, err := r.ranges.Update(ctx, options.ForceRangeRefresh)
	if err != nil {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		state := r.store.Snapshot()
		loader, canLoadCached := r.ranges.(RangeSnapshotLoader)
		if state.NodePool == nil || !canLoadCached {
			return report, fmt.Errorf("update ranges: %w", err)
		}
		cachedSnapshot, loadErr := loader.Load()
		if loadErr != nil {
			return report, errors.Join(fmt.Errorf("update ranges: %w", err), fmt.Errorf("load cached ranges for old node pool: %w", loadErr))
		}
		if cachedSnapshot.Hash != state.NodePool.RangeHash {
			return report, errors.Join(fmt.Errorf("update ranges: %w", err), fmt.Errorf("cached ranges hash %q does not match old node pool %q", cachedSnapshot.Hash, state.NodePool.RangeHash))
		}
		rangeResult = ranges.UpdateResult{Snapshot: cachedSnapshot, Warning: fmt.Sprintf("range refresh failed; using cached snapshot for old node pool: %v", err)}
		r.logger.Warn("官方网段刷新失败，已读取与旧节点池匹配的本地快照", "run_id", report.ID, "range_hash", cachedSnapshot.Hash, "error", err, "result", "degraded")
	}
	report.RangeSource = rangeResult.Snapshot.Source
	report.RangeHash = rangeResult.Snapshot.Hash
	if rangeResult.Warning != "" {
		report.Warnings = append(report.Warnings, rangeResult.Warning)
		r.logger.Warn("网段更新降级", "run_id", report.ID, "error", rangeResult.Warning)
	}
	if options.ApplyPolicy {
		stateBeforeMigration := r.store.Snapshot()
		if hasUnsafeLegacyDomainMappings(stateBeforeMigration.Policy) {
			if err := r.replaceUnsafeLegacyDomainMappings(ctx, stateBeforeMigration); err != nil {
				return report, fmt.Errorf("replace unsafe legacy domain mappings: %w", err)
			}
			warning := "已撤销不符合域名 IP 独占约束的旧策略；新策略失败时将保持正常 DNS"
			report.Warnings = append(report.Warnings, warning)
			r.logger.Warn("旧域名共享映射已安全撤销", "run_id", report.ID, "result", "completed")
		}
	}

	temporaryTransactions, err := r.applyTemporaryRoutes(ctx, rangeResult.Snapshot, options.ApplyPolicy)
	if err != nil {
		return report, err
	}
	defer func() {
		if cleanupErr := r.rollbackRoutes(ctx, temporaryTransactions); cleanupErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("clean temporary routes: %w", cleanupErr))
		}
	}()

	stateBefore := r.store.Snapshot()
	report.Results, report.NodePoolState, report.NodePoolID, report.NodePoolValidUntil, report.BenchmarkPath, err = r.resolveNodePool(ctx, report.ID, rangeResult.Snapshot, stateBefore, options, temporaryTransactions, emit)
	if err != nil {
		return report, err
	}
	if report.NodePoolState == "stale" {
		report.Warnings = append(report.Warnings, "节点池刷新失败，本轮继续使用已过期但通过轻量校验的旧节点池")
	}
	currentStage = stageDomain
	stateBefore = r.store.Snapshot()
	checkpoint := stateBefore.Optimization
	resumeDomain := r.resumableDomainCheckpoint(checkpoint, report.NodePoolID)
	if !resumeDomain {
		if err := r.updateCheckpoint(report.ID, stageDomain, report.NodePoolID, nil); err != nil {
			return report, fmt.Errorf("persist domain stage checkpoint: %w", err)
		}
	}
	r.emit(emit, Event{RunID: report.ID, Type: "stage.started", Stage: stageDomain, Message: "qualifying accelerated domains from node pool"})
	ApplyHistory(report.Results, stateBefore.Nodes)
	sort.SliceStable(report.Results, func(i, j int) bool { return report.Results[i].Score > report.Results[j].Score })
	report.IPv4Decision = Decide(report.Results, stateBefore.CurrentIPv4, 4, r.config.Benchmark, r.now())
	report.IPv6Decision = Decide(report.Results, stateBefore.CurrentIPv6, 6, r.config.Benchmark, r.now())
	r.emit(emit, Event{RunID: report.ID, Type: "selection.completed", Stage: "selection", Message: report.IPv4Decision.Reason + "; " + report.IPv6Decision.Reason})

	var applied proxy.ApplyResult
	var removedRouteTransactions []string
	failedAddresses := make(map[string]struct{})
	if options.ApplyPolicy {
		r.emit(emit, Event{RunID: report.ID, Type: "stage.started", Stage: stagePolicyPlan, Message: "planning accelerated domain policy"})
		// 保留既有 IPC 消费者的 policy 阶段事件，同时提供更细的检查点阶段。
		r.emit(emit, Event{RunID: report.ID, Type: "stage.started", Stage: "policy", Message: "applying and verifying selected policy"})
		failedDomainAddresses := make(map[string]map[string]struct{})
		failedVerificationErrors := make(map[string]error)
		resumedPolicyPlan := false
	retryPolicy:
		for {
			if resumeDomain {
				var resumed bool
				report.domainMappings, report.DomainAllocations, resumed = r.restoreDomainCheckpoint(checkpoint)
				if !resumed {
					resumeDomain = false
					continue retryPolicy
				}
				report.domainAllocationCompleted = true
				resumeDomain = false
				resumedPolicyPlan = true
			} else {
				var allocationWarnings []string
				report.domainMappings, report.DomainAllocations, allocationWarnings, err = r.allocateDomainMappings(ctx, rangeResult.Snapshot, report.Results, r.store.Snapshot(), domainAllocationOptions{
					excludedAddresses: failedDomainAddresses,
				})
				if err != nil {
					return report, fmt.Errorf("allocate accelerated domains: %w", err)
				}
				report.Warnings = append(report.Warnings, allocationWarnings...)
				report.domainAllocationCompleted = true
			}
			if failure := manualDomainAllocationFailure(report); failure != "" {
				if recordErr := r.persistDomainAllocationFailure(report); recordErr != nil {
					return report, errors.Join(errors.New(failure), recordErr)
				}
				currentStage = stageDomain
				return report, errors.New(failure)
			}
			for domain, verificationFailure := range failedVerificationErrors {
				if !isAutomaticDomain(r.config, domain) || !allocationHasNoAddress(report.DomainAllocations, domain) {
					continue
				}
				isolated, isolateErr := r.isolateFailedAutomaticDomain(verificationFailure)
				if isolateErr != nil {
					return report, isolateErr
				}
				if isolated {
					continue retryPolicy
				}
			}
			if !resumedPolicyPlan {
				if err := r.persistPolicyPlanCheckpoint(ctx, report.ID, report.NodePoolID, stateBefore, report); err != nil {
					return report, fmt.Errorf("persist policy plan checkpoint: %w", err)
				}
			}
			resumedPolicyPlan = false
			currentStage = stageApplyVerify
			r.emit(emit, Event{RunID: report.ID, Type: "stage.started", Stage: stageApplyVerify, Message: "applying and verifying selected policy"})
			applied, removedRouteTransactions, err = r.applySelectedPolicy(ctx, stateBefore, report)
			if err == nil {
				break
			}
			resumeDomain = false
			var verificationErr *proxy.DomainVerificationError
			if errors.As(err, &verificationErr) && verificationErr.Address != "" {
				domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(verificationErr.Domain)), ".")
				failedVerificationErrors[domain] = err
				if failedDomainAddresses[domain] == nil {
					failedDomainAddresses[domain] = make(map[string]struct{})
				}
				failedDomainAddresses[domain][verificationErr.Address] = struct{}{}
				if _, recorded := failedAddresses[verificationErr.Address]; !recorded {
					failedAddresses[verificationErr.Address] = struct{}{}
					if cooldownErr := r.recordApplicationVerificationFailure(verificationErr.Address); cooldownErr != nil {
						return report, errors.Join(err, cooldownErr)
					}
				}
				if verificationErr.Domain != "" && isAutomaticDomain(r.config, verificationErr.Domain) && allocationHasNoAddress(report.DomainAllocations, verificationErr.Domain) {
					isolated, isolateErr := r.isolateFailedAutomaticDomain(err)
					if isolateErr != nil {
						return report, errors.Join(err, isolateErr)
					}
					if isolated {
						continue retryPolicy
					}
				}
				continue retryPolicy
			}
			isolated, isolateErr := r.isolateFailedAutomaticDomain(err)
			if isolateErr != nil {
				return report, errors.Join(err, isolateErr)
			}
			if !isolated {
				return report, err
			}
		}
		stateBefore = r.store.Snapshot()
		report.PolicyApplied = true
	}
	currentStage = stageCommit
	r.emit(emit, Event{RunID: report.ID, Type: "stage.started", Stage: stageCommit, Message: "committing optimization state"})
	if err := r.persistSuccessfulRun(ctx, report, stateBefore, applied, options.ApplyPolicy); err != nil {
		currentStage = stageApplyVerify
		if rollbackErr := r.rollbackRoutes(ctx, removedRouteTransactions); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("restore obsolete routes after persistence failure: %w", rollbackErr))
		}
		if options.ApplyPolicy && r.policy != nil {
			rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.config.Network.CommandTimeout.Duration())
			defer cancel()
			if rollbackErr := r.policy.Rollback(rollbackContext, applied); rollbackErr != nil {
				return report, errors.Join(err, fmt.Errorf("rollback unpersisted policy: %w", rollbackErr))
			}
		}
		return report, err
	}
	if len(failedAddresses) > 0 {
		if err := r.reinstateApplicationFailureCooldown(failedAddresses); err != nil {
			return report, err
		}
	}
	return report, nil
}

// resolveNodePool 复用有效节点池，或在刷新失败时验证并降级使用兼容的旧池。
func (r *Runner) resolveNodePool(
	ctx context.Context,
	runID string,
	snapshot ranges.Snapshot,
	state store.State,
	options RunOptions,
	temporaryTransactions []string,
	emit func(Event),
) ([]benchmark.Result, string, string, time.Time, []proxy.BenchmarkPathEvidence, error) {
	now := r.now().UTC()
	benchmarkHash, err := stableHash(r.config.Benchmark)
	if err != nil {
		return nil, "", "", time.Time{}, nil, fmt.Errorf("hash benchmark config: %w", err)
	}
	networkFingerprint, err := r.measureNetworkFingerprint(ctx)
	if err != nil {
		return nil, "", "", time.Time{}, nil, fmt.Errorf("fingerprint physical path: %w", err)
	}
	r.activeNetworkFingerprint = networkFingerprint
	pool := state.NodePool
	refreshInterval := r.config.Schedule.Interval.Duration()
	compatible := nodePoolCompatible(pool, snapshot.Hash, benchmarkHash, networkFingerprint, refreshInterval)
	validator, canValidate := r.benchmark.(NodePoolValidator)
	freshValidationFailed := false
	if !options.ForceRangeRefresh && compatible && now.Before(pool.ValidUntil) {
		r.emit(emit, Event{RunID: runID, Type: "stage.started", Stage: stagePoolReuse, Message: "validating reusable node pool"})
		results := append([]benchmark.Result(nil), pool.Candidates...)
		if canValidate {
			validated, evidence, validateErr := r.validateNodePool(ctx, runID, validator, results, options.ApplyPolicy, temporaryTransactions, emit)
			if validateErr == nil {
				r.logger.Info("节点池已复用", "run_id", runID, "pool_id", pool.ID, "valid_until", pool.ValidUntil, "candidates", len(validated), "result", "fresh")
				return validated, "fresh", pool.ID, pool.ValidUntil, evidence, nil
			}
			if ctx.Err() != nil {
				return nil, "", "", time.Time{}, evidence, ctx.Err()
			}
			freshValidationFailed = true
			r.logger.Warn("有效节点池轻量校验失败，将执行完整刷新", "run_id", runID, "pool_id", pool.ID, "error", validateErr)
		} else {
			r.logger.Warn("节点池验证器不可用，复用持久化节点池", "run_id", runID, "pool_id", pool.ID, "result", "degraded")
			return results, "fresh", pool.ID, pool.ValidUntil, nil, nil
		}
	}

	r.emit(emit, Event{RunID: runID, Type: "stage.started", Stage: stagePoolRefresh, Message: "refreshing benchmark node pool"})
	addresses, err := r.generateCandidates(snapshot, state, now)
	if err != nil {
		return nil, "", "", time.Time{}, nil, err
	}
	r.logger.Info("候选生成完成", "run_id", runID, "candidates", len(addresses), "range_hash", snapshot.Hash)
	finishGuard, evidence, err := r.beginNodePoolProbe(ctx, runID, addresses, options.ApplyPolicy, temporaryTransactions)
	if err != nil {
		return nil, "", "", time.Time{}, nil, err
	}
	results, benchmarkErr := r.benchmark.Run(ctx, addresses, func(progress benchmark.Progress) {
		r.emit(emit, Event{RunID: runID, Type: "benchmark.progress", Stage: string(progress.Stage), Progress: &progress})
	})
	guardErr := finishGuard()
	if guardErr != nil {
		return nil, "", "", time.Time{}, evidence, fmt.Errorf("rollback benchmark DIRECT guard before final policy: %w", guardErr)
	}
	if benchmarkErr == nil {
		sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
		if !hasQualifiedResult(results) {
			if !compatible || freshValidationFailed {
				return results, "unavailable", "", time.Time{}, evidence, nil
			}
			benchmarkErr = errors.New("benchmark produced no qualified node")
		}
		if benchmarkErr == nil {
			poolCandidates := make([]benchmark.Result, 0, len(results))
			for _, result := range results {
				if result.Qualified && result.IP.IsValid() {
					poolCandidates = append(poolCandidates, result)
				}
			}
			validUntil := now.Add(refreshInterval).UTC()
			pool = &store.NodePoolSnapshot{
				Version: store.NodePoolSchemaVersion, ID: newRunID(), CreatedAt: now, ValidUntil: validUntil,
				RefreshInterval: refreshInterval,
				RangeSource:     snapshot.Source, RangeHash: snapshot.Hash, NetworkFingerprint: networkFingerprint,
				BenchmarkConfigHash: benchmarkHash, Candidates: poolCandidates,
			}
			pool.Checksum, err = nodePoolChecksum(pool)
			if err != nil {
				return nil, "", "", time.Time{}, evidence, fmt.Errorf("checksum node pool: %w", err)
			}
			if err := r.persistNodePool(pool); err != nil {
				return nil, "", "", time.Time{}, evidence, fmt.Errorf("persist node pool: %w", err)
			}
			r.logger.Info("节点池刷新完成", "run_id", runID, "pool_id", pool.ID, "valid_until", validUntil, "candidates", len(results), "result", "refreshed")
			return results, "refreshed", pool.ID, validUntil, evidence, nil
		}
	}
	if ctx.Err() != nil {
		return nil, "", "", time.Time{}, evidence, ctx.Err()
	}
	if compatible && !freshValidationFailed {
		staleResults := append([]benchmark.Result(nil), pool.Candidates...)
		var staleEvidence []proxy.BenchmarkPathEvidence
		if canValidate {
			staleResults, staleEvidence, err = r.validateNodePool(ctx, runID, validator, staleResults, options.ApplyPolicy, temporaryTransactions, emit)
			if err != nil {
				return nil, "", "", time.Time{}, append(evidence, staleEvidence...), errors.Join(fmt.Errorf("benchmark candidates: %w", benchmarkErr), fmt.Errorf("validate stale node pool: %w", err))
			}
		}
		r.logger.Warn("节点池刷新失败，已降级复用旧池", "run_id", runID, "pool_id", pool.ID, "error", benchmarkErr, "result", "stale")
		return staleResults, "stale", pool.ID, pool.ValidUntil, append(evidence, staleEvidence...), nil
	}
	return nil, "", "", time.Time{}, evidence, fmt.Errorf("benchmark candidates: %w", benchmarkErr)
}

// validateNodePool 对节点池排名候选执行当前网络下的 TCP/TLS 轻量校验。
func (r *Runner) validateNodePool(
	ctx context.Context,
	runID string,
	validator NodePoolValidator,
	results []benchmark.Result,
	applyPolicy bool,
	temporaryTransactions []string,
	emit func(Event),
) ([]benchmark.Result, []proxy.BenchmarkPathEvidence, error) {
	addresses := qualifiedAddresses(results)
	finishGuard, evidence, err := r.beginNodePoolProbe(ctx, runID, addresses, applyPolicy, temporaryTransactions)
	if err != nil {
		return nil, nil, err
	}
	validated, validateErr := validator.Validate(ctx, results, func(progress benchmark.Progress) {
		r.emit(emit, Event{RunID: runID, Type: "benchmark.progress", Stage: string(progress.Stage), Progress: &progress})
	})
	guardErr := finishGuard()
	if guardErr != nil {
		return nil, evidence, errors.Join(validateErr, fmt.Errorf("clean node pool validation guard: %w", guardErr))
	}
	return validated, evidence, validateErr
}

// beginNodePoolProbe 在需要时建立并验证临时 DIRECT 保护，返回幂等清理函数。
func (r *Runner) beginNodePoolProbe(ctx context.Context, runID string, addresses []netip.Addr, applyPolicy bool, temporaryTransactions []string) (func() error, []proxy.BenchmarkPathEvidence, error) {
	noop := func() error { return nil }
	if !applyPolicy || r.policy == nil || len(addresses) == 0 {
		return noop, nil, nil
	}
	guard, _ := r.policy.(proxy.BenchmarkGuard)
	if guard == nil {
		return noop, nil, nil
	}
	guardResult, err := guard.BeginBenchmarkGuard(ctx, benchmarkDirectPolicy(addresses), addresses)
	if err != nil {
		return noop, nil, fmt.Errorf("apply benchmark DIRECT guard: %w", err)
	}
	evidence := append([]proxy.BenchmarkPathEvidence(nil), guardResult.Evidence...)
	if len(evidence) == 0 {
		physicalEvidence, physicalErr := r.verifyPhysicalBenchmarkFallback(ctx, addresses, temporaryTransactions)
		if physicalErr != nil {
			cleanupErr := r.endBenchmarkGuard(ctx, guard, guardResult)
			return noop, nil, errors.Join(fmt.Errorf("benchmark guard produced no DIRECT evidence: %w", physicalErr), cleanupErr)
		}
		evidence = append(evidence, physicalEvidence)
	}
	for index := range evidence {
		item := &evidence[index]
		if item.ProxyObserved && item.DirectVerified {
			item.PhysicalRouteUsed = len(temporaryTransactions) > 0
		} else if !item.ProxyObserved && item.SocketBound && len(temporaryTransactions) > 0 {
			item.DirectVerified = true
			item.PhysicalRouteUsed = true
			item.Verification = "bound_socket_and_verified_physical_route"
		}
		if !item.DirectVerified {
			cleanupErr := r.endBenchmarkGuard(ctx, guard, guardResult)
			return noop, evidence, errors.Join(fmt.Errorf("benchmark path to %s lacks DIRECT connection or verified physical-route evidence", item.Target), cleanupErr)
		}
		r.logBenchmarkPathEvidence(runID, *item)
	}
	finished := false
	finish := func() error {
		if finished {
			return nil
		}
		finished = true
		return r.endBenchmarkGuard(ctx, guard, guardResult)
	}
	return finish, evidence, nil
}

// verifyPhysicalBenchmarkFallback 要求临时路由已验证，并通过真实物理接口 Socket 补充 DIRECT 证据。
func (r *Runner) verifyPhysicalBenchmarkFallback(ctx context.Context, addresses []netip.Addr, temporaryTransactions []string) (proxy.BenchmarkPathEvidence, error) {
	if len(temporaryTransactions) == 0 {
		return proxy.BenchmarkPathEvidence{}, errors.New("no verified temporary physical route is available")
	}
	verifier, ok := r.benchmark.(physicalBenchmarkPathVerifier)
	if !ok {
		return proxy.BenchmarkPathEvidence{}, errors.New("benchmark does not expose bound physical Socket verification")
	}
	evidence, err := verifier.VerifyPhysicalPath(ctx, addresses)
	if err != nil {
		return proxy.BenchmarkPathEvidence{}, err
	}
	if r.physicalPath.Interface == "" || evidence.Interface != r.physicalPath.Interface || !evidence.SocketBound || !evidence.DirectVerified {
		return proxy.BenchmarkPathEvidence{}, errors.New("physical benchmark evidence does not match the confirmed interface")
	}
	evidence.Adapter = "physical-route"
	evidence.GuardApplied = false
	evidence.PhysicalRouteUsed = true
	if evidence.Verification == "" {
		evidence.Verification = "bound_socket_and_verified_physical_route"
	}
	return evidence, nil
}

// logBenchmarkPathEvidence 记录不含认证信息的测速路径验证摘要。
func (r *Runner) logBenchmarkPathEvidence(runID string, evidence proxy.BenchmarkPathEvidence) {
	r.logger.Info("测速直连路径验证完成", "run_id", runID, "adapter", evidence.Adapter, "interface", evidence.Interface, "target_ip", evidence.Target, "proxy_observed", evidence.ProxyObserved, "physical_route_used", evidence.PhysicalRouteUsed, "result", "verified")
}

func qualifiedAddresses(results []benchmark.Result) []netip.Addr {
	addresses := make([]netip.Addr, 0, len(results))
	seen := make(map[netip.Addr]struct{}, len(results))
	for _, result := range results {
		address := result.IP.Unmap()
		if !result.Qualified || !address.IsValid() {
			continue
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address)
	}
	return addresses
}

func hasQualifiedResult(results []benchmark.Result) bool {
	for _, result := range results {
		if result.Qualified && result.IP.IsValid() {
			return true
		}
	}
	return false
}

func nodePoolCompatible(pool *store.NodePoolSnapshot, rangeHash, benchmarkHash, networkFingerprint string, refreshInterval time.Duration) bool {
	if pool == nil || pool.Version != store.NodePoolSchemaVersion || pool.ID == "" || len(pool.Candidates) == 0 {
		return false
	}
	if pool.RangeHash != rangeHash || pool.BenchmarkConfigHash != benchmarkHash || pool.NetworkFingerprint != networkFingerprint || pool.RefreshInterval != refreshInterval {
		return false
	}
	checksum, err := nodePoolChecksum(pool)
	return err == nil && checksum == pool.Checksum
}

func nodePoolChecksum(pool *store.NodePoolSnapshot) (string, error) {
	if pool == nil {
		return "", errors.New("node pool is required")
	}
	clone := *pool
	clone.Checksum = ""
	return stableHash(clone)
}

// measureNetworkFingerprint 优先读取实时网络摘要，读取失败时使用已发现物理路径摘要降级。
func (r *Runner) measureNetworkFingerprint(ctx context.Context) (string, error) {
	if r.networkFingerprint != nil {
		fingerprint, err := r.networkFingerprint(ctx, r.config.Network.CommandTimeout.Duration())
		if err == nil && fingerprint != "" {
			return fingerprint, nil
		}
		if err != nil {
			r.logger.Warn("实时网络路径指纹读取失败，改用已发现物理路径", "error", err, "result", "degraded")
		}
	}
	return stableHash(r.physicalPath)
}

func (r *Runner) checkpointNetworkFingerprint() (string, error) {
	if r.activeNetworkFingerprint != "" {
		return r.activeNetworkFingerprint, nil
	}
	return stableHash(r.physicalPath)
}

func stableHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// persistNodePool 原子切换活动节点池；失败刷新不会触碰旧池。
func (r *Runner) persistNodePool(pool *store.NodePoolSnapshot) error {
	return r.store.Update(func(state *store.State) error {
		state.NodePool = pool
		if state.Optimization != nil && state.Optimization.PoolID != pool.ID {
			state.Optimization = nil
		}
		return nil
	})
}

// updateCheckpoint 原子保存阶段证据，并保留同一节点池上一次失败的域名证据。
func (r *Runner) updateCheckpoint(runID, stage, poolID string, evidence []store.DomainEvidence) error {
	configHash, err := r.optimizationConfigHash()
	if err != nil {
		return err
	}
	networkFingerprint, err := r.checkpointNetworkFingerprint()
	if err != nil {
		return err
	}
	now := r.now().UTC()
	return r.store.Update(func(state *store.State) error {
		previous := state.Optimization
		checkpoint := &store.OptimizationCheckpoint{Version: store.OptimizationCheckpointVersion, RunID: runID, CurrentStage: stage, PoolID: poolID, ConfigHash: configHash, NetworkFingerprint: networkFingerprint, UpdatedAt: now, Attempts: map[string]int{}}
		if previous != nil && previous.PoolID == poolID && previous.ConfigHash == configHash && previous.NetworkFingerprint == networkFingerprint && previous.CurrentStage != stageDomain {
			checkpoint.DomainEvidence = append([]store.DomainEvidence(nil), previous.DomainEvidence...)
			checkpoint.PolicyPlan = append(json.RawMessage(nil), previous.PolicyPlan...)
			for key, value := range previous.Attempts {
				checkpoint.Attempts[key] = value
			}
		}
		if evidence != nil {
			checkpoint.DomainEvidence = append([]store.DomainEvidence(nil), evidence...)
		}
		checkpoint.Attempts[stage]++
		state.Optimization = checkpoint
		return nil
	})
}

// persistPolicyPlanCheckpoint 保存已完成域名证据和待应用策略计划，应用失败时可跳过域名复测。
func (r *Runner) persistPolicyPlanCheckpoint(ctx context.Context, runID, poolID string, state store.State, report RunReport) error {
	if r.policy == nil {
		return errors.New("policy application requested but no adapter is configured")
	}
	finalPolicy, err := r.policyForDecisionsWithContext(ctx, state, report, false)
	if err != nil {
		return err
	}
	transitionPolicy, err := r.policyForDecisionsWithContext(ctx, state, report, true)
	if err != nil {
		return err
	}
	planned, err := json.Marshal(struct {
		Final      proxy.DirectPolicy `json:"final"`
		Transition proxy.DirectPolicy `json:"transition"`
	}{Final: finalPolicy, Transition: transitionPolicy})
	if err != nil {
		return err
	}
	evidence := domainEvidenceFromAllocations(report.DomainAllocations, r.now().UTC().Add(domainEvidenceLifetime))
	if err := r.updateCheckpoint(runID, stagePolicyPlan, poolID, evidence); err != nil {
		return err
	}
	return r.store.Update(func(state *store.State) error {
		if state.Optimization == nil {
			return errors.New("optimization checkpoint disappeared while saving policy plan")
		}
		state.Optimization.PolicyPlan = planned
		return nil
	})
}

// markCheckpointFailure 记录当前阶段错误，让下一次一键优选能判断可复用检查点。
func (r *Runner) markCheckpointFailure(runID, stage string, operationErr error) error {
	if operationErr == nil {
		return nil
	}
	configHash, err := r.optimizationConfigHash()
	if err != nil {
		return err
	}
	networkFingerprint, err := r.checkpointNetworkFingerprint()
	if err != nil {
		return err
	}
	return r.store.Update(func(state *store.State) error {
		checkpoint := state.Optimization
		if checkpoint == nil || checkpoint.Version != store.OptimizationCheckpointVersion {
			checkpoint = &store.OptimizationCheckpoint{Version: store.OptimizationCheckpointVersion, Attempts: map[string]int{}}
		}
		checkpoint.RunID = runID
		checkpoint.CurrentStage = stage
		checkpoint.ConfigHash = configHash
		checkpoint.NetworkFingerprint = networkFingerprint
		checkpoint.LastError = operationErr.Error()
		checkpoint.UpdatedAt = r.now().UTC()
		if checkpoint.Attempts == nil {
			checkpoint.Attempts = map[string]int{}
		}
		checkpoint.Attempts[stage]++
		state.Optimization = checkpoint
		return nil
	})
}

func (r *Runner) optimizationConfigHash() (string, error) {
	return stableHash(struct {
		Benchmark    config.BenchmarkConfig    `json:"benchmark"`
		Network      config.NetworkConfig      `json:"network"`
		Acceleration config.AccelerationConfig `json:"acceleration"`
		Proxy        config.ProxyConfig        `json:"proxy"`
	}{Benchmark: r.config.Benchmark, Network: r.config.Network, Acceleration: r.config.Acceleration, Proxy: r.config.Proxy})
}

func (r *Runner) resumableDomainCheckpoint(checkpoint *store.OptimizationCheckpoint, poolID string) bool {
	if checkpoint == nil || poolID == "" || checkpoint.PoolID != poolID || (checkpoint.CurrentStage != stagePolicyPlan && checkpoint.CurrentStage != stageApplyVerify) {
		return false
	}
	configHash, err := r.optimizationConfigHash()
	if err != nil || checkpoint.ConfigHash != configHash {
		return false
	}
	networkFingerprint, err := r.checkpointNetworkFingerprint()
	if err != nil || checkpoint.NetworkFingerprint != networkFingerprint || len(checkpoint.DomainEvidence) == 0 {
		return false
	}
	now := r.now()
	for _, evidence := range checkpoint.DomainEvidence {
		if evidence.AssignedAddress == "" || evidence.ValidUntil.Before(now) || !evidence.CloudflareVerified || !evidence.PreflightVerified {
			return false
		}
		if evidence.DownloadVerified && evidence.DownloadMbps < r.config.Acceleration.ManualDownloadMinMbps {
			return false
		}
	}
	return true
}

func (r *Runner) restoreDomainCheckpoint(checkpoint *store.OptimizationCheckpoint) ([]proxy.DomainMapping, []DomainAllocationResult, bool) {
	if checkpoint == nil || len(checkpoint.DomainEvidence) == 0 {
		return nil, nil, false
	}
	mappings := make([]proxy.DomainMapping, 0, len(checkpoint.DomainEvidence))
	allocations := make([]DomainAllocationResult, 0, len(checkpoint.DomainEvidence))
	for _, evidence := range checkpoint.DomainEvidence {
		if evidence.AssignedAddress == "" {
			return nil, nil, false
		}
		mappings = append(mappings, proxy.DomainMapping{Domain: evidence.Domain, Addresses: []string{evidence.AssignedAddress}})
		allocations = append(allocations, DomainAllocationResult{Domain: evidence.Domain, Source: evidence.Source, ResolvedAddresses: append([]string(nil), evidence.ResolvedAddresses...), AssignedAddress: evidence.AssignedAddress, DownloadAddress: evidence.DownloadAddress, DownloadProbeURL: evidence.DownloadProbeURL, CloudflareVerified: evidence.CloudflareVerified, PreflightVerified: evidence.PreflightVerified, DownloadVerified: evidence.DownloadVerified, DownloadMbps: evidence.DownloadMbps})
	}
	return mappings, allocations, true
}

func domainEvidenceFromAllocations(allocations []DomainAllocationResult, validUntil time.Time) []store.DomainEvidence {
	now := time.Now().UTC()
	evidence := make([]store.DomainEvidence, 0, len(allocations))
	for _, allocation := range allocations {
		evidence = append(evidence, store.DomainEvidence{Domain: allocation.Domain, Source: allocation.Source, ResolvedAddresses: append([]string(nil), allocation.ResolvedAddresses...), AssignedAddress: allocation.AssignedAddress, CloudflareVerified: allocation.CloudflareVerified, PreflightVerified: allocation.PreflightVerified, DownloadVerified: allocation.DownloadVerified, DownloadMbps: allocation.DownloadMbps, DownloadAddress: allocation.DownloadAddress, DownloadProbeURL: allocation.DownloadProbeURL, TestedAt: now, ValidUntil: validUntil, Error: allocation.Error})
	}
	return evidence
}

// benchmarkDirectPolicy 将本次实际候选收敛为精确主机规则，避免临时保护扩大范围。
func benchmarkDirectPolicy(addresses []netip.Addr) proxy.DirectPolicy {
	policy := proxy.DirectPolicy{}
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() {
			continue
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		prefix := netip.PrefixFrom(address, address.BitLen()).String()
		if address.Is4() {
			policy.IPv4CIDRs = append(policy.IPv4CIDRs, prefix)
		} else {
			policy.IPv6CIDRs = append(policy.IPv6CIDRs, prefix)
		}
	}
	return policy
}

// endBenchmarkGuard 使用独立清理上下文撤销临时代理规则，任务取消也不得遗留配置。
func (r *Runner) endBenchmarkGuard(ctx context.Context, guard proxy.BenchmarkGuard, result proxy.BenchmarkGuardResult) error {
	if guard == nil || len(result.Receipts) == 0 {
		return nil
	}
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.config.Network.CommandTimeout.Duration())
	defer cancel()
	return guard.EndBenchmarkGuard(cleanupContext, result)
}

// replaceUnsafeLegacyDomainMappings 前向应用无域名映射的安全策略，摆脱已失效的历史收据链。
func (r *Runner) replaceUnsafeLegacyDomainMappings(ctx context.Context, before store.State) error {
	if r.policy == nil {
		return errors.New("unsafe legacy domain mappings cannot be replaced because no adapter is configured")
	}
	report := RunReport{domainAllocationCompleted: true}
	safePolicy, err := r.policyForDecisionsWithContext(ctx, before, report, false)
	if err != nil {
		return err
	}
	applied, err := r.policy.Apply(ctx, safePolicy)
	if err != nil {
		return fmt.Errorf("apply safe policy without domain mappings: %w", err)
	}
	removedRouteTransactions, err := r.removeObsoletePolicyRoutes(ctx, before.Policy, safePolicy)
	if err != nil {
		if rollbackErr := r.policy.Rollback(ctx, applied); rollbackErr != nil {
			err = errors.Join(err, rollbackErr)
		}
		return err
	}
	receipts, err := json.Marshal(applied)
	if err != nil {
		_ = r.rollbackRoutes(ctx, removedRouteTransactions)
		_ = r.policy.Rollback(ctx, applied)
		return err
	}
	if err := r.store.Update(func(state *store.State) error {
		state.Policy = policySnapshot(safePolicy, receipts, r.now().UTC())
		state.PendingPolicy = nil
		return nil
	}); err != nil {
		if restoreErr := r.rollbackRoutes(ctx, removedRouteTransactions); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
		if rollbackErr := r.policy.Rollback(ctx, applied); rollbackErr != nil {
			err = errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
}

// hasUnsafeLegacyDomainMappings 检测需要在新策略应用前撤销的旧版共享或多地址映射。
func hasUnsafeLegacyDomainMappings(policy *store.PolicySnapshot) bool {
	if policy == nil {
		return false
	}
	assignedDomains := make(map[string]struct{}, len(policy.DomainMappings))
	assignedAddresses := make(map[netip.Addr]string, len(policy.DomainMappings))
	for _, mapping := range policy.DomainMappings {
		domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(mapping.Domain)), ".")
		if domain == "" || len(mapping.Addresses) != 1 {
			return true
		}
		if _, exists := assignedDomains[domain]; exists {
			return true
		}
		assignedDomains[domain] = struct{}{}
		address, err := netip.ParseAddr(mapping.Addresses[0])
		if err != nil {
			return true
		}
		address = address.Unmap()
		if previousDomain, exists := assignedAddresses[address]; exists && previousDomain != domain {
			return true
		}
		assignedAddresses[address] = domain
	}
	return false
}

// CleanupManagedPolicy 逆序撤销全部已持久化收据，并仅在完整成功后清空当前策略状态。
func (r *Runner) CleanupManagedPolicy(ctx context.Context) error {
	return CleanupManagedPolicy(ctx, r.store, r.routes, r.policy)
}

// RefreshPolicy 使用当前已选节点重新应用域名集合，供自动发现和订阅刷新恢复调用。
func (r *Runner) RefreshPolicy(ctx context.Context) error {
	if !r.tryAcquireMaintenance() {
		return ErrAlreadyRunning
	}
	defer r.operationGate.release()
	return r.refreshPolicyLocked(ctx)
}

// ClearDiscoveredDomains 仅删除自动发现记录，并先验证仅保留手动域名的新策略。
func (r *Runner) ClearDiscoveredDomains(ctx context.Context) (DiscoveredDomainCleanupResult, error) {
	if !r.tryAcquireMaintenance() {
		return DiscoveredDomainCleanupResult{}, ErrAlreadyRunning
	}
	defer r.operationGate.release()

	before := r.store.Snapshot()
	manualDomains := make(map[string]struct{}, len(r.config.AccelerationDomains()))
	for _, domain := range r.config.AccelerationDomains() {
		manualDomains[domain] = struct{}{}
	}
	automaticDomains := make(map[string]struct{}, len(before.DiscoveredDomains))
	for domain, record := range before.DiscoveredDomains {
		if _, manual := manualDomains[domain]; manual || record.Source == "manual" {
			continue
		}
		automaticDomains[domain] = struct{}{}
	}
	result := DiscoveredDomainCleanupResult{Cleared: len(automaticDomains)}
	if result.Cleared == 0 {
		return result, nil
	}
	if before.Policy != nil {
		for _, mapping := range before.Policy.DomainMappings {
			if _, automatic := automaticDomains[mapping.Domain]; automatic {
				result.AccelerationsRemoved++
			}
		}
	}
	clearDiscoveries := func(state *store.State) {
		for domain := range automaticDomains {
			delete(state.DiscoveredDomains, domain)
		}
	}
	if result.AccelerationsRemoved == 0 {
		if err := r.store.Update(func(state *store.State) error {
			clearDiscoveries(state)
			return nil
		}); err != nil {
			return DiscoveredDomainCleanupResult{}, fmt.Errorf("clear discovered domains: %w", err)
		}
		r.logger.Info("自动发现域名已清理", "cleared", result.Cleared, "accelerations_removed", 0, "policy_refreshed", false, "result", "completed")
		return result, nil
	}
	if r.policy == nil {
		return DiscoveredDomainCleanupResult{}, errors.New("cannot remove discovered domain acceleration because no policy adapter is configured")
	}
	if err := r.refreshPolicyWithStateMutationLocked(ctx, clearDiscoveries); err != nil {
		return DiscoveredDomainCleanupResult{}, fmt.Errorf("remove discovered domain acceleration: %w", err)
	}
	result.PolicyRefreshed = true
	r.logger.Info("自动发现域名已清理", "cleared", result.Cleared, "accelerations_removed", result.AccelerationsRemoved, "policy_refreshed", true, "result", "completed")
	return result, nil
}

// UpdateAccelerationDomains 在单任务边界内替换域名集合，并在活动适配器可用时刷新当前策略。
func (r *Runner) UpdateAccelerationDomains(ctx context.Context, manualDomains, excludedDomains []string) (bool, error) {
	if !r.tryAcquireMaintenance() {
		return false, ErrAlreadyRunning
	}
	defer r.operationGate.release()
	if slices.Equal(r.config.Acceleration.ManualDomains, manualDomains) && slices.Equal(r.config.Acceleration.ExcludedDomains, excludedDomains) {
		return false, nil
	}
	previousManualDomains := append([]string(nil), r.config.Acceleration.ManualDomains...)
	previousExcludedDomains := append([]string(nil), r.config.Acceleration.ExcludedDomains...)
	r.config.Acceleration.ManualDomains = append([]string(nil), manualDomains...)
	r.config.Acceleration.ExcludedDomains = append([]string(nil), excludedDomains...)
	policySnapshot := r.store.Snapshot().Policy
	if policySnapshot == nil {
		r.logger.Info("域名加速配置已热更新", "manual_domains", len(manualDomains), "excluded_domains", len(excludedDomains), "policy_refreshed", false, "result", "completed")
		return false, nil
	}
	if r.policy == nil {
		r.logger.Warn("域名加速配置已保存但当前策略未刷新", "manual_domains", len(manualDomains), "excluded_domains", len(excludedDomains), "policy_refreshed", false, "policy_snapshot_present", true, "result", "deferred")
		return false, nil
	}
	if err := r.refreshPolicyLocked(ctx); err != nil {
		r.config.Acceleration.ManualDomains = previousManualDomains
		r.config.Acceleration.ExcludedDomains = previousExcludedDomains
		return false, fmt.Errorf("refresh policy after acceleration domain update: %w", err)
	}
	r.logger.Info("域名加速配置已热更新", "manual_domains", len(manualDomains), "excluded_domains", len(excludedDomains), "policy_refreshed", true, "result", "completed")
	return true, nil
}

// tryAcquireMaintenance 仅在没有完整优选排队时允许后台维护立即取得执行权。
func (r *Runner) tryAcquireMaintenance() bool {
	if r.pendingRuns.Load() > 0 || !r.operationGate.tryAcquire() {
		return false
	}
	if r.pendingRuns.Load() > 0 {
		r.operationGate.release()
		return false
	}
	return true
}

// TryPolicyMaintenance 在不排队抢占完整优选的前提下执行一次窄策略维护操作。
func (r *Runner) TryPolicyMaintenance(ctx context.Context, maintain func(context.Context) error) error {
	if maintain == nil {
		return errors.New("policy maintenance callback is required")
	}
	if !r.tryAcquireMaintenance() {
		return ErrAlreadyRunning
	}
	defer r.operationGate.release()
	if err := ctx.Err(); err != nil {
		return err
	}
	return maintain(ctx)
}

// refreshPolicyLocked 使用已持有的单任务锁刷新策略，避免配置切换期间并行运行测速。
func (r *Runner) refreshPolicyLocked(ctx context.Context) error {
	return r.refreshPolicyWithStateMutationLocked(ctx, nil)
}

// refreshPolicyWithStateMutationLocked 先用变更后的状态验证策略，再把策略和状态变更原子提交。
func (r *Runner) refreshPolicyWithStateMutationLocked(ctx context.Context, mutateState func(*store.State)) error {
	return r.refreshPolicyWithManualMappingsLocked(ctx, nil, mutateState)
}

// refreshPolicyWithManualMappingsLocked 在维护流程中保留当前已应用映射，或使用调用方确认的显式映射。
func (r *Runner) refreshPolicyWithManualMappingsLocked(ctx context.Context, manualMappingOverrides map[string]string, mutateState func(*store.State)) error {
	if r.policy == nil {
		return errors.New("policy refresh requested but no adapter is configured")
	}
	rangeResult, err := r.ranges.Update(ctx, false)
	if err != nil {
		return fmt.Errorf("load ranges for policy refresh: %w", err)
	}
	failedDomainAddresses := make(map[string]map[string]struct{})
	failedAddresses := make(map[string]struct{})
	failedVerificationErrors := make(map[string]error)
retryRefresh:
	for {
		before := r.store.Snapshot()
		planned := before
		if mutateState != nil {
			mutateState(&planned)
		}
		ranked := rankedHistoricalResults(planned, r.now())
		allocationOverrides := manualMappingOverrides
		if allocationOverrides == nil {
			allocationOverrides = currentManualMappingOverrides(r.config, planned.Policy)
		}
		mappings, allocations, allocationWarnings, allocationErr := r.allocateDomainMappings(ctx, rangeResult.Snapshot, ranked, planned, domainAllocationOptions{
			manualMappingOverrides: allocationOverrides,
			excludedAddresses:      failedDomainAddresses,
		})
		if allocationErr != nil {
			return fmt.Errorf("allocate accelerated domains during policy refresh: %w", allocationErr)
		}
		for _, warning := range allocationWarnings {
			r.logger.Warn("域名未分配优选 IP", "warning", warning)
		}
		report := RunReport{DomainAllocations: allocations, domainMappings: mappings, domainAllocationCompleted: true}
		if failure := manualDomainAllocationFailure(report); failure != "" {
			if manualMappingOverrides != nil && hasDomainMappingCapabilityFailure(report.DomainAllocations) {
				return fmt.Errorf("%w: %s", proxy.ErrDomainMappingsUnavailable, failure)
			}
			if recordErr := r.persistDomainAllocationFailure(report); recordErr != nil {
				return errors.Join(errors.New(failure), recordErr)
			}
			return errors.New(failure)
		}
		for domain, verificationFailure := range failedVerificationErrors {
			if !isAutomaticDomain(r.config, domain) || !allocationHasNoAddress(allocations, domain) {
				continue
			}
			isolated, isolateErr := r.isolateFailedAutomaticDomain(verificationFailure)
			if isolateErr != nil {
				return isolateErr
			}
			if isolated {
				continue retryRefresh
			}
		}
		policy, policyErr := r.policyForDecisionsWithContext(ctx, planned, report, false)
		if policyErr != nil {
			return policyErr
		}
		applied, applyErr := r.policy.Apply(ctx, policy)
		if applyErr != nil {
			var verificationErr *proxy.DomainVerificationError
			if errors.As(applyErr, &verificationErr) && verificationErr.Address != "" {
				domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(verificationErr.Domain)), ".")
				failedVerificationErrors[domain] = applyErr
				if failedDomainAddresses[domain] == nil {
					failedDomainAddresses[domain] = make(map[string]struct{})
				}
				failedDomainAddresses[domain][verificationErr.Address] = struct{}{}
				if _, recorded := failedAddresses[verificationErr.Address]; !recorded {
					failedAddresses[verificationErr.Address] = struct{}{}
					if cooldownErr := r.recordApplicationVerificationFailure(verificationErr.Address); cooldownErr != nil {
						return errors.Join(applyErr, cooldownErr)
					}
				}
				if isAutomaticDomain(r.config, verificationErr.Domain) && allocationHasNoAddress(allocations, verificationErr.Domain) {
					isolated, isolateErr := r.isolateFailedAutomaticDomain(applyErr)
					if isolateErr != nil {
						return errors.Join(applyErr, isolateErr)
					}
					if isolated {
						continue retryRefresh
					}
				}
				continue retryRefresh
			}
			isolated, isolateErr := r.isolateFailedAutomaticDomain(applyErr)
			if isolateErr != nil {
				return errors.Join(fmt.Errorf("refresh policy: %w", applyErr), isolateErr)
			}
			if isolated {
				continue
			}
			return fmt.Errorf("refresh policy: %w", applyErr)
		}
		newApplied := applied
		if len(policy.DomainMappings) > 0 {
			if r.domainVerifier == nil {
				if rollbackErr := r.policy.Rollback(ctx, applied); rollbackErr != nil {
					return errors.Join(errors.New("domain mapping verification is unavailable"), rollbackErr)
				}
				return errors.New("domain mapping verification is unavailable")
			}
			if verifyErr := r.domainVerifier.VerifyApplied(ctx, policy.DomainMappings); verifyErr != nil {
				if rollbackErr := r.policy.Rollback(ctx, applied); rollbackErr != nil {
					verifyErr = errors.Join(verifyErr, rollbackErr)
				}
				var verificationErr *proxy.DomainVerificationError
				if errors.As(verifyErr, &verificationErr) && verificationErr.Address != "" {
					domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(verificationErr.Domain)), ".")
					failedVerificationErrors[domain] = verifyErr
					if failedDomainAddresses[domain] == nil {
						failedDomainAddresses[domain] = make(map[string]struct{})
					}
					failedDomainAddresses[domain][verificationErr.Address] = struct{}{}
					if _, recorded := failedAddresses[verificationErr.Address]; !recorded {
						failedAddresses[verificationErr.Address] = struct{}{}
						if cooldownErr := r.recordApplicationVerificationFailure(verificationErr.Address); cooldownErr != nil {
							return errors.Join(verifyErr, cooldownErr)
						}
					}
					if isAutomaticDomain(r.config, verificationErr.Domain) && allocationHasNoAddress(allocations, verificationErr.Domain) {
						isolated, isolateErr := r.isolateFailedAutomaticDomain(verifyErr)
						if isolateErr != nil {
							return errors.Join(verifyErr, isolateErr)
						}
						if isolated {
							continue retryRefresh
						}
					}
					continue retryRefresh
				}
				isolated, isolateErr := r.isolateFailedAutomaticDomain(verifyErr)
				if isolateErr != nil {
					return errors.Join(fmt.Errorf("verify refreshed domains: %w", verifyErr), isolateErr)
				}
				if isolated {
					continue
				}
				return fmt.Errorf("verify refreshed domains: %w", verifyErr)
			}
		}
		removedRouteTransactions, removeErr := r.removeObsoletePolicyRoutes(ctx, before.Policy, policy)
		if removeErr != nil {
			if rollbackErr := r.policy.Rollback(ctx, newApplied); rollbackErr != nil {
				removeErr = errors.Join(removeErr, rollbackErr)
			}
			return fmt.Errorf("remove obsolete policy routes: %w", removeErr)
		}
		if before.Policy != nil {
			var previous proxy.ApplyResult
			if decodeErr := json.Unmarshal(before.Policy.Receipts, &previous); decodeErr != nil {
				_ = r.rollbackRoutes(ctx, removedRouteTransactions)
				_ = r.policy.Rollback(ctx, newApplied)
				return fmt.Errorf("decode previous policy receipts: %w", decodeErr)
			}
			applied.Receipts = append(previous.Receipts, applied.Receipts...)
		}
		receipts, marshalErr := json.Marshal(applied)
		if marshalErr != nil {
			_ = r.rollbackRoutes(ctx, removedRouteTransactions)
			_ = r.policy.Rollback(ctx, newApplied)
			return marshalErr
		}
		if persistErr := r.store.Update(func(state *store.State) error {
			if mutateState != nil {
				mutateState(state)
			}
			state.Policy = policySnapshot(policy, receipts, r.now().UTC())
			recordDomainAllocationResults(state, report.DomainAllocations, true, r.now().UTC())
			state.PendingPolicy = nil
			return nil
		}); persistErr != nil {
			if restoreErr := r.rollbackRoutes(ctx, removedRouteTransactions); restoreErr != nil {
				persistErr = errors.Join(persistErr, restoreErr)
			}
			if rollbackErr := r.policy.Rollback(ctx, newApplied); rollbackErr != nil {
				return errors.Join(persistErr, rollbackErr)
			}
			return persistErr
		}
		if err := r.reinstateApplicationFailureCooldown(failedAddresses); err != nil {
			return err
		}
		return nil
	}
}

// CleanupManagedPolicy 恢复路由事务和累计适配器收据，供运行器与卸载专用运行时复用。
func CleanupManagedPolicy(ctx context.Context, stateStore *store.Store, routes *cfnetwork.RouteController, policy PolicyApplier) error {
	if stateStore == nil {
		return errors.New("state store is required for managed policy cleanup")
	}
	if routes != nil {
		if err := routes.Recover(ctx); err != nil {
			return fmt.Errorf("recover temporary routes: %w", err)
		}
	}
	if err := RecoverPendingPolicy(ctx, stateStore, policy); err != nil {
		return err
	}
	snapshot := stateStore.Snapshot()
	if snapshot.Policy == nil {
		return nil
	}
	if policy == nil {
		return errors.New("stored policy cannot be cleaned because its adapters are disabled")
	}
	var applied proxy.ApplyResult
	if err := json.Unmarshal(snapshot.Policy.Receipts, &applied); err != nil {
		return fmt.Errorf("decode stored policy receipts: %w", err)
	}
	if err := cleanupAppliedPolicy(ctx, policy, applied); err != nil {
		return fmt.Errorf("rollback stored policy: %w", err)
	}
	return stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = nil
		state.CurrentIPv6 = nil
		state.Policy = nil
		state.PendingPolicy = nil
		return nil
	})
}

// RecoverPendingPolicy 回滚尚未提交的适配器收据，并仅在完整成功后清除事务日志。
func RecoverPendingPolicy(ctx context.Context, stateStore *store.Store, policy PolicyApplier) error {
	if stateStore == nil {
		return errors.New("state store is required for pending policy recovery")
	}
	pending := stateStore.Snapshot().PendingPolicy
	if pending == nil {
		return nil
	}
	applied, err := decodeApplyResult(pending.Receipts)
	if err != nil {
		return fmt.Errorf("decode pending policy receipts: %w", err)
	}
	if len(applied.Receipts) > 0 {
		if policy == nil {
			return errors.New("pending policy cannot be recovered because its adapters are disabled")
		}
		if err := cleanupAppliedPolicy(ctx, policy, applied); err != nil {
			return fmt.Errorf("rollback pending policy: %w", err)
		}
	}
	return stateStore.Update(func(state *store.State) error {
		state.PendingPolicy = nil
		return nil
	})
}

func cleanupAppliedPolicy(ctx context.Context, policy PolicyApplier, applied proxy.ApplyResult) error {
	if cleaner, ok := policy.(managedPolicyCleaner); ok {
		return cleaner.Cleanup(ctx, applied)
	}
	return policy.Rollback(ctx, applied)
}

func (r *Runner) generateCandidates(snapshot ranges.Snapshot, state store.State, now time.Time) ([]netip.Addr, error) {
	var preferred []netip.Addr
	for _, current := range []*store.Selection{state.CurrentIPv4, state.CurrentIPv6} {
		if current != nil {
			if address, err := netip.ParseAddr(current.IP); err == nil {
				preferred = append(preferred, address)
			}
		}
	}
	var cooldown []netip.Addr
	for rawAddress, stats := range state.Nodes {
		if stats.CooldownUntil.After(now) {
			if address, err := netip.ParseAddr(rawAddress); err == nil {
				cooldown = append(cooldown, address)
			}
		}
	}
	ipv4Enabled := r.config.Benchmark.IPv4
	ipv6Enabled := r.config.Benchmark.IPv6
	if r.config.Network.ManageRoutes {
		ipv4Enabled = ipv4Enabled && r.physicalPath.GatewayIPv4 != ""
		ipv6Enabled = ipv6Enabled && r.physicalPath.GatewayIPv6 != ""
	}
	return candidates.Generate(snapshot, candidates.Options{
		Count: r.config.Benchmark.Candidates, IPv4: ipv4Enabled, IPv6: ipv6Enabled,
		Seed: candidates.DailySeed(now, r.config.Benchmark.DailySeed), Preferred: preferred, Cooldown: cooldown,
	})
}

func (r *Runner) applyTemporaryRoutes(ctx context.Context, snapshot ranges.Snapshot, shouldApply bool) ([]string, error) {
	if !shouldApply || !r.config.Network.ManageRoutes {
		return nil, nil
	}
	if r.routes == nil {
		return nil, errors.New("route management is enabled but no route controller is configured")
	}
	ipv4Enabled := r.config.Benchmark.IPv4 && r.physicalPath.GatewayIPv4 != ""
	ipv6Enabled := r.config.Benchmark.IPv6 && r.physicalPath.GatewayIPv6 != ""
	prefixes, err := snapshot.Prefixes(ipv4Enabled, ipv6Enabled)
	if err != nil {
		return nil, err
	}
	var transactionIDs []string
	for _, prefix := range prefixes {
		gateway := r.physicalPath.GatewayIPv6
		if prefix.Addr().Is4() {
			gateway = r.physicalPath.GatewayIPv4
		}
		if gateway == "" {
			continue
		}
		route := cfnetwork.RouteSpec{Prefix: prefix.String(), Gateway: gateway, Interface: r.physicalPath.Interface, InterfaceIndex: r.physicalPath.InterfaceIndex, Metric: managedRouteMetric}
		plan, err := r.routes.Plan(ctx, route, true)
		if err != nil {
			_ = r.rollbackRoutes(ctx, transactionIDs)
			return nil, err
		}
		transaction, err := r.routes.Apply(ctx, plan)
		if err != nil {
			_ = r.rollbackRoutes(ctx, transactionIDs)
			return nil, err
		}
		transactionIDs = append(transactionIDs, transaction.ID)
	}
	return transactionIDs, nil
}

// rollbackRoutes 为每条路由提供独立清理时限，并忽略已取消的任务上下文以防残留。
func (r *Runner) rollbackRoutes(ctx context.Context, transactionIDs []string) error {
	if r.routes == nil {
		return nil
	}
	cleanupContext := context.WithoutCancel(ctx)
	cleanupTimeout := r.routeCleanupTimeout()
	var rollbackErrors []error
	for index := len(transactionIDs) - 1; index >= 0; index-- {
		transactionContext, cancel := context.WithTimeout(cleanupContext, cleanupTimeout)
		err := r.routes.Rollback(transactionContext, transactionIDs[index])
		cancel()
		if err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	return errors.Join(rollbackErrors...)
}

// routeCleanupTimeout 为 Windows 路由回滚保留独立下限，避免 PowerShell 启动抖动中断清理。
func (r *Runner) routeCleanupTimeout() time.Duration {
	configured := r.config.Network.CommandTimeout.Duration()
	if configured < routeCleanupTimeoutFloor {
		return routeCleanupTimeoutFloor
	}
	return configured
}

// applySelectedPolicy 构造节点与域名联合策略，并在应用前后分别完成 SNI 和系统映射验证。
func (r *Runner) applySelectedPolicy(ctx context.Context, state store.State, report RunReport) (proxy.ApplyResult, []string, error) {
	if r.policy == nil {
		return proxy.ApplyResult{}, nil, errors.New("policy application requested but no adapter is configured")
	}
	finalPolicy, err := r.policyForDecisionsWithContext(ctx, state, report, false)
	if err != nil {
		return proxy.ApplyResult{}, nil, err
	}
	transitionPolicy, err := r.policyForDecisionsWithContext(ctx, state, report, true)
	if err != nil {
		return proxy.ApplyResult{}, nil, err
	}
	var transition proxy.ApplyResult
	if policiesDiffer(finalPolicy, transitionPolicy) {
		transition, err = r.policy.Apply(ctx, transitionPolicy)
		if err != nil {
			return proxy.ApplyResult{}, nil, fmt.Errorf("apply transition policy: %w", err)
		}
	}
	finalResult, err := r.policy.Apply(ctx, finalPolicy)
	if err != nil {
		if rollbackErr := r.policy.Rollback(ctx, transition); rollbackErr != nil {
			err = errors.Join(err, rollbackErr)
		}
		return proxy.ApplyResult{}, nil, fmt.Errorf("apply final policy: %w", err)
	}
	combined := proxy.ApplyResult{
		Receipts: append(append([]proxy.Receipt{}, transition.Receipts...), finalResult.Receipts...),
		Skipped:  append(append([]string{}, transition.Skipped...), finalResult.Skipped...),
	}
	if len(finalPolicy.DomainMappings) > 0 {
		if r.domainVerifier == nil {
			if rollbackErr := r.policy.Rollback(ctx, combined); rollbackErr != nil {
				return proxy.ApplyResult{}, nil, errors.Join(errors.New("domain mapping verification is unavailable"), rollbackErr)
			}
			return proxy.ApplyResult{}, nil, errors.New("domain mapping verification is unavailable")
		}
		if err := r.domainVerifier.VerifyApplied(ctx, finalPolicy.DomainMappings); err != nil {
			if rollbackErr := r.policy.Rollback(ctx, combined); rollbackErr != nil {
				err = errors.Join(err, rollbackErr)
			}
			return proxy.ApplyResult{}, nil, fmt.Errorf("verify optimized domains after apply: %w", err)
		}
	}
	removedRoutes, err := r.removeObsoletePolicyRoutes(ctx, state.Policy, finalPolicy)
	if err != nil {
		if rollbackErr := r.policy.Rollback(ctx, combined); rollbackErr != nil {
			err = errors.Join(err, rollbackErr)
		}
		return proxy.ApplyResult{}, nil, fmt.Errorf("remove obsolete policy routes: %w", err)
	}
	return combined, removedRoutes, nil
}

// isolateFailedAutomaticDomain 停用真实连接验证失败的单个自动域名，允许剩余策略继续应用。
func (r *Runner) isolateFailedAutomaticDomain(operationErr error) (bool, error) {
	var verificationErr *proxy.DomainVerificationError
	if !errors.As(operationErr, &verificationErr) || verificationErr.Domain == "" {
		return false, nil
	}
	failedDomain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(verificationErr.Domain)), ".")
	for _, manualDomain := range r.config.AccelerationDomains() {
		if manualDomain == failedDomain {
			return false, nil
		}
	}
	if !automaticDomainAllocationEnabled(r.config) {
		return false, nil
	}
	updated := false
	if err := r.store.Update(func(state *store.State) error {
		discovery, exists := state.DiscoveredDomains[failedDomain]
		if !exists {
			return nil
		}
		discovery.Active = false
		discovery.PreflightVerified = false
		discovery.LastError = fmt.Sprintf("代理实际连接验证失败: %v", verificationErr.Err)
		state.DiscoveredDomains[failedDomain] = discovery
		updated = true
		return nil
	}); err != nil {
		return false, fmt.Errorf("deactivate failed automatic domain %s: %w", failedDomain, err)
	}
	if updated {
		r.logger.Warn("自动域名实际连接验证失败，已隔离并重试策略", "domain", failedDomain, "error", verificationErr.Err)
	}
	return updated, nil
}

// isAutomaticDomain 判断域名是否来自自动发现而非用户手动配置。
func isAutomaticDomain(cfg config.Config, rawDomain string) bool {
	if !automaticDomainAllocationEnabled(cfg) {
		return false
	}
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rawDomain)), ".")
	for _, manualDomain := range cfg.AccelerationDomains() {
		if manualDomain == domain {
			return false
		}
	}
	return true
}

// allocationHasNoAddress 判断指定域名在本轮分配中是否已经耗尽候选地址。
func allocationHasNoAddress(allocations []DomainAllocationResult, rawDomain string) bool {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rawDomain)), ".")
	for _, allocation := range allocations {
		if strings.TrimSuffix(strings.ToLower(strings.TrimSpace(allocation.Domain)), ".") == domain {
			return allocation.AssignedAddress == ""
		}
	}
	return true
}

// recordApplicationVerificationFailure 将应用验证失败的候选立即加入持久化冷却列表。
func (r *Runner) recordApplicationVerificationFailure(rawAddress string) error {
	address := strings.TrimSpace(rawAddress)
	if address == "" {
		return nil
	}
	now := r.now().UTC()
	return r.store.Update(func(state *store.State) error {
		if state.Nodes == nil {
			state.Nodes = make(map[string]store.NodeStats)
		}
		stats := state.Nodes[address]
		stats.FailureStreak++
		stats.CooldownUntil = now.Add(r.config.Benchmark.FailureCooldown.Duration()).UTC()
		state.Nodes[address] = stats
		return nil
	})
}

// reinstateApplicationFailureCooldown 在成功运行持久化测速结果后恢复失败候选的冷却状态。
func (r *Runner) reinstateApplicationFailureCooldown(addresses map[string]struct{}) error {
	if len(addresses) == 0 {
		return nil
	}
	now := r.now().UTC()
	return r.store.Update(func(state *store.State) error {
		if state.Nodes == nil {
			state.Nodes = make(map[string]store.NodeStats)
		}
		for address := range addresses {
			stats := state.Nodes[address]
			if stats.FailureStreak < 1 {
				stats.FailureStreak = 1
			}
			cooldownUntil := now.Add(r.config.Benchmark.FailureCooldown.Duration()).UTC()
			if stats.CooldownUntil.Before(cooldownUntil) {
				stats.CooldownUntil = cooldownUntil
			}
			state.Nodes[address] = stats
		}
		return nil
	})
}

// verifyCloudflareDomain 要求物理 DNS 返回的全部地址都属于当前可信网段快照，并返回可审计地址。
func (r *Runner) verifyCloudflareDomain(ctx context.Context, snapshot ranges.Snapshot, domain string) ([]netip.Addr, error) {
	addresses, err := r.domainResolver.Resolve(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("resolve acceleration domain %s: %w", domain, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("acceleration domain %s has no public address", domain)
	}
	for _, address := range addresses {
		if !snapshot.Contains(address) {
			return nil, fmt.Errorf("acceleration domain %s resolved outside verified Cloudflare ranges: %s", domain, address)
		}
	}
	return addresses, nil
}

// allocateDomainMappings 先为手动域名执行目标资源下载复测，再让自动发现域名消费剩余兼容地址。
func (r *Runner) allocateDomainMappings(ctx context.Context, snapshot ranges.Snapshot, results []benchmark.Result, state store.State, options domainAllocationOptions) ([]proxy.DomainMapping, []DomainAllocationResult, []string, error) {
	if !r.config.Acceleration.Enabled {
		return nil, nil, nil, nil
	}
	candidatesByDomain := make([]domainAllocationCandidate, 0, len(r.config.Acceleration.ManualDomains))
	for _, domain := range r.config.AccelerationDomains() {
		candidatesByDomain = append(candidatesByDomain, domainAllocationCandidate{domain: domain, source: "manual"})
	}
	if automaticDomainAllocationEnabled(r.config) {
		for _, discovery := range acceleration.EffectiveDiscoveries(r.config, state) {
			candidatesByDomain = append(candidatesByDomain, domainAllocationCandidate{domain: discovery.Domain, source: "automatic"})
		}
	}
	if len(candidatesByDomain) == 0 {
		return nil, nil, nil, nil
	}
	if r.policy == nil || !r.activePolicyCapabilities(ctx).DomainMappings {
		allocations := failedDomainAllocations(candidatesByDomain, domainMappingCapabilityUnavailableReason)
		return nil, allocations, domainAllocationWarnings(allocations), nil
	}
	if r.domainResolver == nil || r.domainVerifier == nil {
		allocations := failedDomainAllocations(candidatesByDomain, "verification is unavailable")
		return nil, allocations, domainAllocationWarnings(allocations), nil
	}
	ranked := append([]benchmark.Result(nil), results...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	candidates := make([]string, 0, len(ranked))
	seen := make(map[netip.Addr]struct{}, len(ranked))
	for _, result := range ranked {
		address := result.IP.Unmap()
		if !result.Qualified || !address.IsValid() || !snapshot.Contains(address) {
			continue
		}
		if r.config.Network.ManageRoutes && ((address.Is4() && r.physicalPath.GatewayIPv4 == "") || (address.Is6() && r.physicalPath.GatewayIPv6 == "")) {
			continue
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		candidates = append(candidates, address.String())
	}
	var mappings []proxy.DomainMapping
	allocations := make([]DomainAllocationResult, 0, len(candidatesByDomain))
	assignedCandidates := make(map[string]struct{}, len(candidatesByDomain))
	preflightRejectedCandidates := make(map[string]struct{})
	for _, candidate := range candidatesByDomain {
		allocation := DomainAllocationResult{Domain: candidate.domain, Source: candidate.source}
		requiresDownload := candidate.source == "manual" && r.config.Acceleration.ManualDownloadTest
		if requiresDownload && r.domainDownload == nil {
			allocation.Error = "manual domain download verification is unavailable"
			allocations = append(allocations, allocation)
			continue
		}
		excludedForDomain := map[string]struct{}{}
		if options.excludedAddresses != nil {
			excludedForDomain = options.excludedAddresses[candidate.domain]
		}
		resolvedAddresses, err := r.verifyCloudflareDomain(ctx, snapshot, candidate.domain)
		if err != nil {
			allocation.Error = err.Error()
			allocations = append(allocations, allocation)
			continue
		}
		allocation.CloudflareVerified = true
		for _, address := range resolvedAddresses {
			allocation.ResolvedAddresses = append(allocation.ResolvedAddresses, address.Unmap().String())
		}
		candidatePool := candidates
		explicitAddress, hasExplicitAddress := "", false
		if candidate.source == "manual" {
			explicitAddress, hasExplicitAddress = options.manualMappingOverrides[normalizeDomainForMapping(candidate.domain)]
			if hasExplicitAddress {
				explicitAddress = strings.TrimSpace(explicitAddress)
				parsedAddress, parseErr := netip.ParseAddr(explicitAddress)
				if parseErr != nil || !parsedAddress.IsGlobalUnicast() || parsedAddress.IsPrivate() || parsedAddress.IsLoopback() || parsedAddress.IsLinkLocalUnicast() || !snapshot.Contains(parsedAddress.Unmap()) {
					allocation.Error = fmt.Sprintf("configured manual mapping %s is not a current public Cloudflare address", explicitAddress)
					allocations = append(allocations, allocation)
					continue
				}
				parsedAddress = parsedAddress.Unmap()
				if r.config.Network.ManageRoutes && ((parsedAddress.Is4() && r.physicalPath.GatewayIPv4 == "") || (parsedAddress.Is6() && r.physicalPath.GatewayIPv6 == "")) {
					allocation.Error = fmt.Sprintf("configured manual mapping %s has no physical gateway", explicitAddress)
					allocations = append(allocations, allocation)
					continue
				}
				explicitAddress = parsedAddress.String()
				candidatePool = []string{explicitAddress}
			}
		}
		var lastError error
		assigned := false
		probeURL := ""
		for _, candidateAddress := range candidatePool {
			if _, alreadyAssigned := assignedCandidates[candidateAddress]; alreadyAssigned {
				continue
			}
			if _, rejected := preflightRejectedCandidates[candidateAddress]; rejected {
				continue
			}
			if _, excluded := excludedForDomain[candidateAddress]; excluded {
				continue
			}
			transactionID := ""
			if requiresDownload {
				transactionID, err = r.applyDomainProbeRoute(ctx, candidateAddress)
				if err != nil {
					return nil, nil, nil, fmt.Errorf("prepare direct download route for %s via %s: %w", candidate.domain, candidateAddress, err)
				}
			}
			mapping := proxy.DomainMapping{Domain: candidate.domain, Addresses: []string{candidateAddress}}
			preflightErr := r.domainVerifier.VerifyPreflight(ctx, []proxy.DomainMapping{mapping})
			if preflightErr == nil && requiresDownload {
				if probeURL == "" {
					probeURL, preflightErr = r.domainDownload.DiscoverProbeURL(ctx, candidate.domain, candidateAddress)
				}
				if preflightErr == nil {
					var downloadResult acceleration.DownloadResult
					downloadResult, preflightErr = r.domainDownload.Measure(ctx, candidate.domain, candidateAddress, probeURL)
					if downloadResult.Mbps > allocation.DownloadMbps {
						allocation.DownloadMbps = downloadResult.Mbps
					}
					allocation.DownloadAddress = candidateAddress
					allocation.DownloadProbeURL = probeURL
					if preflightErr == nil && downloadResult.Mbps < r.config.Acceleration.ManualDownloadMinMbps {
						preflightErr = fmt.Errorf("direct download %.2f Mbps is below configured %.2f Mbps", downloadResult.Mbps, r.config.Acceleration.ManualDownloadMinMbps)
					}
				}
			}
			if transactionID != "" {
				if rollbackErr := r.rollbackRoutes(ctx, []string{transactionID}); rollbackErr != nil {
					return nil, nil, nil, fmt.Errorf("clean direct download route for %s via %s: %w", candidate.domain, candidateAddress, rollbackErr)
				}
			}
			if preflightErr != nil {
				lastError = preflightErr
				if !requiresDownload {
					preflightRejectedCandidates[candidateAddress] = struct{}{}
				}
				continue
			}
			mappings = append(mappings, mapping)
			assignedCandidates[candidateAddress] = struct{}{}
			allocation.AssignedAddress = mapping.Addresses[0]
			allocation.PreflightVerified = true
			allocation.DownloadVerified = requiresDownload
			assigned = true
			break
		}
		if assigned {
			allocations = append(allocations, allocation)
			continue
		}
		if lastError != nil {
			allocation.Error = lastError.Error()
		} else if hasExplicitAddress {
			allocation.Error = fmt.Sprintf("configured manual mapping %s did not pass verification", explicitAddress)
		} else {
			allocation.Error = "ranked address pool is exhausted"
		}
		allocations = append(allocations, allocation)
	}
	return mappings, allocations, domainAllocationWarnings(allocations), nil
}

// currentManualMappingOverrides 从最后一份已验证策略提取手动域名映射，供后台维护原样复用。
func currentManualMappingOverrides(cfg config.Config, policy *store.PolicySnapshot) map[string]string {
	if policy == nil {
		return map[string]string{}
	}
	manualDomains := make(map[string]struct{}, len(cfg.AccelerationDomains()))
	for _, domain := range cfg.AccelerationDomains() {
		manualDomains[normalizeDomainForMapping(domain)] = struct{}{}
	}
	overrides := make(map[string]string, len(manualDomains))
	for _, mapping := range policy.DomainMappings {
		domain := normalizeDomainForMapping(mapping.Domain)
		if _, manual := manualDomains[domain]; !manual || len(mapping.Addresses) != 1 {
			continue
		}
		overrides[domain] = strings.TrimSpace(mapping.Addresses[0])
	}
	return overrides
}

// applyDomainProbeRoute 为单个手动域名候选建立可回滚的物理主机路由。
func (r *Runner) applyDomainProbeRoute(ctx context.Context, rawAddress string) (string, error) {
	if !r.config.Network.ManageRoutes {
		return "", nil
	}
	if r.routes == nil {
		return "", errors.New("route management is enabled but no route controller is configured")
	}
	address, err := netip.ParseAddr(rawAddress)
	if err != nil {
		return "", err
	}
	address = address.Unmap()
	gateway := r.physicalPath.GatewayIPv6
	if address.Is4() {
		gateway = r.physicalPath.GatewayIPv4
	}
	if gateway == "" {
		return "", fmt.Errorf("physical gateway is unavailable for %s", address)
	}
	bits := 128
	if address.Is4() {
		bits = 32
	}
	route := cfnetwork.RouteSpec{
		Prefix: netip.PrefixFrom(address, bits).String(), Gateway: gateway,
		Interface: r.physicalPath.Interface, InterfaceIndex: r.physicalPath.InterfaceIndex, Metric: managedRouteMetric,
	}
	plan, err := r.routes.Plan(ctx, route, true)
	if err != nil {
		return "", err
	}
	transaction, err := r.routes.Apply(ctx, plan)
	if err != nil {
		return "", err
	}
	return transaction.ID, nil
}

// failedDomainAllocations 为无法进入校验流程的域名生成逐项失败结果。
func failedDomainAllocations(candidates []domainAllocationCandidate, reason string) []DomainAllocationResult {
	allocations := make([]DomainAllocationResult, 0, len(candidates))
	for _, candidate := range candidates {
		allocations = append(allocations, DomainAllocationResult{Domain: candidate.domain, Source: candidate.source, Error: reason})
	}
	return allocations
}

// domainAllocationWarnings 保留兼容的运行警告，同时以结构化结果作为唯一事实来源。
func domainAllocationWarnings(allocations []DomainAllocationResult) []string {
	var warnings []string
	for _, allocation := range allocations {
		if allocation.AssignedAddress == "" {
			warnings = append(warnings, fmt.Sprintf("acceleration domain %s was not assigned: %s", allocation.Domain, allocation.Error))
		}
	}
	return warnings
}

// manualDomainAllocationFailure 汇总手动域名失败，供任务日志、历史和快速结果保持一致语义。
func manualDomainAllocationFailure(report RunReport) string {
	var failures []string
	for _, allocation := range report.DomainAllocations {
		if allocation.Source == "manual" && allocation.AssignedAddress == "" {
			failures = append(failures, fmt.Sprintf("%s (%s)", allocation.Domain, allocation.Error))
		}
	}
	if len(failures) == 0 {
		return ""
	}
	return "手动域名未全部生效: " + strings.Join(failures, "; ")
}

// hasDomainMappingCapabilityFailure 判断手动映射是否仅因适配器能力暂时不可用而失败。
func hasDomainMappingCapabilityFailure(allocations []DomainAllocationResult) bool {
	for _, allocation := range allocations {
		if allocation.Source == "manual" && allocation.AssignedAddress == "" && allocation.Error == domainMappingCapabilityUnavailableReason {
			return true
		}
	}
	return false
}

// automaticDomainAllocationEnabled 要求三个开关同时开启后才允许自动域名消费剩余地址池。
func automaticDomainAllocationEnabled(cfg config.Config) bool {
	return cfg.Acceleration.Enabled && cfg.Acceleration.AutoDiscover && cfg.Acceleration.AutoApply
}

// rankedHistoricalResults 从仍健康的历史节点重建自动发现刷新所需的稳定排名池。
func rankedHistoricalResults(state store.State, now time.Time) []benchmark.Result {
	results := make([]benchmark.Result, 0, len(state.Nodes))
	seen := make(map[netip.Addr]struct{}, len(state.Nodes))
	for rawAddress, stats := range state.Nodes {
		address, err := netip.ParseAddr(rawAddress)
		if err != nil || stats.Successes == 0 || stats.AverageScore <= 0 || stats.CooldownUntil.After(now) {
			continue
		}
		address = address.Unmap()
		seen[address] = struct{}{}
		family := 6
		if address.Is4() {
			family = 4
		}
		results = append(results, benchmark.Result{IP: address, Family: family, Qualified: true, Score: stats.AverageScore})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].IP.Less(results[j].IP)
	})
	if state.Policy == nil {
		return results
	}
	fallbackScore := float64(-1)
	for _, mapping := range state.Policy.DomainMappings {
		for _, rawAddress := range mapping.Addresses {
			address, err := netip.ParseAddr(rawAddress)
			if err != nil {
				continue
			}
			address = address.Unmap()
			if _, exists := seen[address]; exists {
				continue
			}
			seen[address] = struct{}{}
			family := 6
			if address.Is4() {
				family = 4
			}
			results = append(results, benchmark.Result{IP: address, Family: family, Qualified: true, Score: fallbackScore})
			fallbackScore--
		}
	}
	return results
}

// policyForDecisions 保留测试和旧调用方的静态能力入口。
func (r *Runner) policyForDecisions(state store.State, report RunReport, includePrevious bool) (proxy.DirectPolicy, error) {
	if r.policy == nil {
		return proxy.DirectPolicy{}, errors.New("policy application requested but no adapter is configured")
	}
	return r.policyForDecisionsWithCapabilities(state, report, includePrevious, r.policy.Capabilities())
}

// policyForDecisionsWithContext 使用当前检测成功的适配器能力生成策略。
func (r *Runner) policyForDecisionsWithContext(ctx context.Context, state store.State, report RunReport, includePrevious bool) (proxy.DirectPolicy, error) {
	if r.policy == nil {
		return proxy.DirectPolicy{}, errors.New("policy application requested but no adapter is configured")
	}
	return r.policyForDecisionsWithCapabilities(state, report, includePrevious, r.activePolicyCapabilities(ctx))
}

func (r *Runner) policyForDecisionsWithCapabilities(state store.State, report RunReport, includePrevious bool, capabilities proxy.Capabilities) (proxy.DirectPolicy, error) {
	policy := proxy.DirectPolicy{}
	if capabilities.Processes {
		policy.Processes = []string{"cf-optimizer", "cf-optimizerd", "cf-optimizer.exe", "cf-optimizerd.exe"}
	}
	appendAddress := func(raw string) {
		address, err := netip.ParseAddr(raw)
		if err != nil {
			return
		}
		if address.Is4() {
			policy.IPv4CIDRs = append(policy.IPv4CIDRs, netip.PrefixFrom(address, 32).String())
		} else {
			policy.IPv6CIDRs = append(policy.IPv6CIDRs, netip.PrefixFrom(address, 128).String())
		}
	}
	for _, decision := range []Decision{report.IPv4Decision, report.IPv6Decision} {
		if decision.HasSelection {
			appendAddress(decision.Selected.IP.String())
		}
	}
	if !report.IPv4Decision.HasSelection && state.CurrentIPv4 != nil {
		appendAddress(state.CurrentIPv4.IP)
	}
	if !report.IPv6Decision.HasSelection && state.CurrentIPv6 != nil {
		appendAddress(state.CurrentIPv6.IP)
	}
	if includePrevious {
		if state.Policy != nil {
			policy.IPv4CIDRs = append(policy.IPv4CIDRs, state.Policy.IPv4CIDRs...)
			policy.IPv6CIDRs = append(policy.IPv6CIDRs, state.Policy.IPv6CIDRs...)
		}
	}
	domainMappings := report.domainMappings
	if !report.domainAllocationCompleted {
		domainMappings = storedDomainMappings(r.config, state)
	}
	if len(domainMappings) > 0 {
		if !capabilities.DomainMappings {
			return proxy.DirectPolicy{}, proxy.ErrDomainMappingsUnavailable
		}
		for _, mapping := range domainMappings {
			if capabilities.Domains {
				policy.Domains = append(policy.Domains, mapping.Domain)
			}
			policy.DomainMappings = append(policy.DomainMappings, mapping)
			for _, address := range mapping.Addresses {
				appendAddress(address)
			}
		}
	}
	if len(policy.IPv4CIDRs)+len(policy.IPv6CIDRs) == 0 {
		return proxy.DirectPolicy{}, errors.New("no qualified, current, or assigned IP is available for policy application")
	}
	normalized, err := policy.Normalize()
	if err != nil {
		return proxy.DirectPolicy{}, err
	}
	return normalized, nil
}

// storedDomainMappings 按手动优先、自动随后顺序复用上次已经完成验证的域名分配。
func storedDomainMappings(cfg config.Config, state store.State) []proxy.DomainMapping {
	if state.Policy == nil {
		return nil
	}
	stored := make(map[string]store.DomainMappingSnapshot, len(state.Policy.DomainMappings))
	for _, mapping := range state.Policy.DomainMappings {
		stored[mapping.Domain] = mapping
	}
	var result []proxy.DomainMapping
	domains := cfg.AccelerationDomains()
	if automaticDomainAllocationEnabled(cfg) {
		for _, discovery := range acceleration.EffectiveDiscoveries(cfg, state) {
			domains = append(domains, discovery.Domain)
		}
	}
	for _, domain := range domains {
		mapping, exists := stored[domain]
		if !exists || len(mapping.Addresses) == 0 {
			continue
		}
		result = append(result, proxy.DomainMapping{Domain: domain, Addresses: append([]string(nil), mapping.Addresses...)})
	}
	return result
}

func policiesDiffer(left, right proxy.DirectPolicy) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) != string(rightJSON)
}

// obsoletePolicyPrefixes 返回旧策略中已不再使用的精确主机路由。
func obsoletePolicyPrefixes(previous *store.PolicySnapshot, next proxy.DirectPolicy) []string {
	if previous == nil {
		return nil
	}
	nextPrefixes := make(map[netip.Prefix]struct{}, len(next.IPv4CIDRs)+len(next.IPv6CIDRs))
	for _, rawPrefix := range append(append([]string(nil), next.IPv4CIDRs...), next.IPv6CIDRs...) {
		prefix, err := netip.ParsePrefix(rawPrefix)
		if err != nil {
			continue
		}
		nextPrefixes[prefix.Masked()] = struct{}{}
	}
	seen := make(map[netip.Prefix]struct{}, len(previous.IPv4CIDRs)+len(previous.IPv6CIDRs))
	var obsolete []string
	for _, rawPrefix := range append(append([]string(nil), previous.IPv4CIDRs...), previous.IPv6CIDRs...) {
		prefix, err := netip.ParsePrefix(rawPrefix)
		if err != nil {
			continue
		}
		prefix = prefix.Masked()
		if prefix.Bits() != prefix.Addr().BitLen() {
			continue
		}
		if _, exists := nextPrefixes[prefix]; exists {
			continue
		}
		if _, exists := seen[prefix]; exists {
			continue
		}
		seen[prefix] = struct{}{}
		obsolete = append(obsolete, prefix.String())
	}
	return obsolete
}

// removeObsoletePolicyRoutes 事务化删除不再属于当前策略的精确主机路由。
func (r *Runner) removeObsoletePolicyRoutes(ctx context.Context, previous *store.PolicySnapshot, next proxy.DirectPolicy) ([]string, error) {
	if !r.config.Network.ManageRoutes || r.routes == nil {
		return nil, nil
	}
	var transactionIDs []string
	for _, rawPrefix := range obsoletePolicyPrefixes(previous, next) {
		prefix := netip.MustParsePrefix(rawPrefix)
		gateway := r.physicalPath.GatewayIPv6
		if prefix.Addr().Is4() {
			gateway = r.physicalPath.GatewayIPv4
		}
		if gateway == "" {
			rollbackErr := r.rollbackRoutes(ctx, transactionIDs)
			return nil, errors.Join(fmt.Errorf("remove route %s: physical gateway is unavailable", prefix), rollbackErr)
		}
		route := cfnetwork.RouteSpec{Prefix: prefix.String(), Gateway: gateway, Interface: r.physicalPath.Interface, InterfaceIndex: r.physicalPath.InterfaceIndex, Metric: managedRouteMetric}
		transaction, err := r.routes.Remove(ctx, route)
		if err != nil {
			rollbackErr := r.rollbackRoutes(ctx, transactionIDs)
			return nil, errors.Join(fmt.Errorf("remove route %s: %w", prefix, err), rollbackErr)
		}
		if transaction.Previous != nil {
			transactionIDs = append(transactionIDs, transaction.ID)
		}
	}
	return transactionIDs, nil
}

func (r *Runner) persistSuccessfulRun(ctx context.Context, report RunReport, before store.State, applied proxy.ApplyResult, policyApplied bool) error {
	now := r.now().UTC()
	details, err := json.Marshal(report.Results)
	if err != nil {
		return err
	}
	if err := r.store.SaveRunDetail(report.ID, details, r.config.History.DetailRetention.Duration()); err != nil {
		return err
	}
	if err := r.store.Update(func(state *store.State) error {
		RecordResults(state.Nodes, report.Results, r.config.Benchmark, now)
		if policyApplied {
			state.CurrentIPv4 = updateSelection(before.CurrentIPv4, report.IPv4Decision, now)
			state.CurrentIPv6 = updateSelection(before.CurrentIPv6, report.IPv6Decision, now)
			if before.Policy != nil {
				var previous proxy.ApplyResult
				if err := json.Unmarshal(before.Policy.Receipts, &previous); err != nil {
					return fmt.Errorf("decode previous policy receipts: %w", err)
				}
				applied.Receipts = append(previous.Receipts, applied.Receipts...)
			}
			receipts, err := json.Marshal(applied)
			if err != nil {
				return err
			}
			policy, err := r.policyForDecisionsWithContext(ctx, before, report, false)
			if err != nil {
				return err
			}
			state.Policy = policySnapshot(policy, receipts, now)
			state.PendingPolicy = nil
		}
		recordDomainAllocationResults(state, report.DomainAllocations, policyApplied, now)
		state.Optimization = nil
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// persistDomainAllocationFailure 记录本次域名分配失败，但不触碰上一份已验证策略。
func (r *Runner) persistDomainAllocationFailure(report RunReport) error {
	now := r.now().UTC()
	return r.store.Update(func(state *store.State) error {
		recordDomainAllocationResults(state, report.DomainAllocations, false, now)
		return nil
	})
}

// recordDomainAllocationResults 将手动与自动域名的本次分配证据写入统一展示状态。
func recordDomainAllocationResults(state *store.State, allocations []DomainAllocationResult, policyApplied bool, now time.Time) {
	if state.DiscoveredDomains == nil {
		state.DiscoveredDomains = make(map[string]store.DomainDiscovery)
	}
	for _, allocation := range allocations {
		record := state.DiscoveredDomains[allocation.Domain]
		record.Domain = allocation.Domain
		record.Source = allocation.Source
		if record.FirstSeenAt.IsZero() {
			record.FirstSeenAt = now
		}
		record.LastSeenAt = now
		previouslyActive := domainMappingPresent(state.Policy, allocation.Domain)
		if policyApplied || !previouslyActive {
			record.CloudflareVerified = allocation.CloudflareVerified
			record.PreflightVerified = allocation.PreflightVerified
			if allocation.DownloadAddress != "" || !previouslyActive {
				record.DownloadVerified = allocation.DownloadVerified
				record.DownloadMbps = allocation.DownloadMbps
				record.DownloadAddress = allocation.DownloadAddress
				record.DownloadProbeURL = allocation.DownloadProbeURL
				record.DownloadTestedAt = now
			}
		}
		record.Active = (policyApplied && allocation.AssignedAddress != "") || (!policyApplied && previouslyActive)
		record.LastResolvedAddresses = append([]string(nil), allocation.ResolvedAddresses...)
		record.LastError = allocation.Error
		if record.Active && policyApplied {
			record.LastError = ""
		}
		state.DiscoveredDomains[allocation.Domain] = record
	}
}

// domainMappingPresent 判断上一份已验证策略是否仍包含指定域名映射。
func domainMappingPresent(policy *store.PolicySnapshot, rawDomain string) bool {
	if policy == nil {
		return false
	}
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rawDomain)), ".")
	for _, mapping := range policy.DomainMappings {
		if strings.TrimSuffix(strings.ToLower(strings.TrimSpace(mapping.Domain)), ".") == domain && len(mapping.Addresses) > 0 {
			return true
		}
	}
	return false
}

// policySnapshot 将规范化策略和累计回滚收据转换为可持久化状态。
func policySnapshot(policy proxy.DirectPolicy, receipts json.RawMessage, appliedAt time.Time) *store.PolicySnapshot {
	mappings := make([]store.DomainMappingSnapshot, 0, len(policy.DomainMappings))
	for _, mapping := range policy.DomainMappings {
		mappings = append(mappings, store.DomainMappingSnapshot{Domain: mapping.Domain, Addresses: append([]string(nil), mapping.Addresses...)})
	}
	return &store.PolicySnapshot{
		IPv4CIDRs: policy.IPv4CIDRs, IPv6CIDRs: policy.IPv6CIDRs, Domains: policy.Domains,
		DomainMappings: mappings, Processes: policy.Processes, Receipts: receipts, AppliedAt: appliedAt,
	}
}

func updateSelection(current *store.Selection, decision Decision, now time.Time) *store.Selection {
	if !decision.HasSelection {
		if current != nil {
			copy := *current
			copy.ConsecutiveFailures++
			return &copy
		}
		return nil
	}
	selectedAt := now
	if current != nil && current.IP == decision.Selected.IP.String() {
		selectedAt = current.SelectedAt
	}
	return &store.Selection{
		IP: decision.Selected.IP.String(), Family: decision.Family, Score: decision.Selected.Score,
		SelectedAt: selectedAt, LastSuccessfulAt: now, ConsecutiveFailures: 0, PolicyVerified: true,
	}
}

func (r *Runner) finalize(report RunReport, runErr error) error {
	return r.store.Update(func(state *store.State) error {
		state.Running = false
		state.LastEndedAt = report.FinishedAt
		cancelled := errors.Is(runErr, context.Canceled)
		if cancelled {
			state.LastError = ""
		} else if runErr != nil {
			state.LastError = runErr.Error()
		}
		summary := store.RunSummary{
			ID: report.ID, StartedAt: report.StartedAt, FinishedAt: report.FinishedAt,
			Candidates: len(report.Results), SwitchReason: report.IPv4Decision.Reason + "; " + report.IPv6Decision.Reason,
		}
		if runErr == nil {
			summary.Error = manualDomainAllocationFailure(report)
		}
		for _, result := range report.Results {
			if result.Qualified {
				summary.Qualified++
			}
			if len(summary.Best) < 10 {
				summary.Best = append(summary.Best, store.ResultSummary{IP: result.IP.String(), Score: result.Score, AvgLatency: result.AvgLatency, Loss: result.Loss, Mbps: result.Mbps})
			}
		}
		if report.IPv4Decision.HasSelection {
			summary.SelectedIPv4 = report.IPv4Decision.Selected.IP.String()
		}
		if report.IPv6Decision.HasSelection {
			summary.SelectedIPv6 = report.IPv6Decision.Selected.IP.String()
		}
		if cancelled {
			summary.Error = "optimization was cancelled"
		} else if runErr != nil {
			summary.Error = runErr.Error()
		}
		state.History = append(state.History, summary)
		return nil
	})
}

func (r *Runner) emit(emitter func(Event), event Event) {
	if emitter == nil {
		return
	}
	event.Timestamp = r.now().UTC()
	emitter(event)
}

func newRunID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("run-%d", time.Now().UnixNano())
}
