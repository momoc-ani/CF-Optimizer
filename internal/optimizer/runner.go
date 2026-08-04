package optimizer

import (
	"context"
	"crypto/rand"
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

// RangeSource 为运行器提供不可变网段快照，便于测试替换远程来源。
type RangeSource interface {
	Update(context.Context, bool) (ranges.UpdateResult, error)
}

// Benchmarker 隔离两阶段测速实现。
type Benchmarker interface {
	Run(context.Context, []netip.Addr, func(benchmark.Progress)) ([]benchmark.Result, error)
}

// PolicyApplier 隔离代理策略协调器。
type PolicyApplier interface {
	Capabilities() proxy.Capabilities
	Apply(context.Context, proxy.DirectPolicy) (proxy.ApplyResult, error)
	Rollback(context.Context, proxy.ApplyResult) error
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

// DomainAllocationResult 记录单个加速域名从物理 DNS 校验到优选地址分配的结果。
type DomainAllocationResult struct {
	Domain             string   `json:"domain"`
	Source             string   `json:"source"`
	ResolvedAddresses  []string `json:"resolved_addresses,omitempty"`
	AssignedAddress    string   `json:"assigned_address,omitempty"`
	CloudflareVerified bool     `json:"cloudflare_verified"`
	PreflightVerified  bool     `json:"preflight_verified"`
	Error              string   `json:"error,omitempty"`
}

type domainAllocationCandidate struct {
	domain string
	source string
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
	domainMappings            []proxy.DomainMapping
	domainAllocationCompleted bool
}

// Runner 协调网段、候选、测速、稳定选择、路由和代理策略的完整事务。
type Runner struct {
	config         config.Config
	ranges         RangeSource
	benchmark      Benchmarker
	store          *store.Store
	routes         *cfnetwork.RouteController
	physicalPath   cfnetwork.PhysicalPath
	policy         PolicyApplier
	domainVerifier DomainMappingVerifier
	domainResolver DomainResolver
	logger         *slog.Logger
	now            func() time.Time
	runMutex       sync.Mutex
}

// SetDomainMappingVerifier 注入生产环境的物理接口 HTTPS 验证器。
func (r *Runner) SetDomainMappingVerifier(verifier DomainMappingVerifier) {
	r.domainVerifier = verifier
}

// SetDomainResolver 注入绕过代理 Fake-IP 的物理 DNS 解析器。
func (r *Runner) SetDomainResolver(resolver DomainResolver) {
	r.domainResolver = resolver
}

// NewRunner 创建依赖显式注入的优选运行器。
func NewRunner(cfg config.Config, rangeSource RangeSource, benchmarker Benchmarker, stateStore *store.Store, routes *cfnetwork.RouteController, physicalPath cfnetwork.PhysicalPath, policy PolicyApplier, logger *slog.Logger) (*Runner, error) {
	if rangeSource == nil || benchmarker == nil || stateStore == nil || logger == nil {
		return nil, errors.New("range source, benchmarker, store and logger are required")
	}
	// Go 接口可能携带 nil 具体指针；在构造边界归一化，避免无适配器配置被误判为可应用策略。
	if policy != nil {
		policyValue := reflect.ValueOf(policy)
		if policyValue.Kind() == reflect.Pointer && policyValue.IsNil() {
			policy = nil
		}
	}
	return &Runner{
		config: cfg, ranges: rangeSource, benchmark: benchmarker, store: stateStore,
		routes: routes, physicalPath: physicalPath, policy: policy,
		logger: logger.With("component", "optimizer"), now: time.Now,
	}, nil
}

// Run 执行一次可取消优选；同一 Runner 同时只允许一个任务。
func (r *Runner) Run(ctx context.Context, options RunOptions, emit func(Event)) (report RunReport, runErr error) {
	if !r.runMutex.TryLock() {
		return RunReport{}, ErrAlreadyRunning
	}
	defer r.runMutex.Unlock()
	report.ID = newRunID()
	report.StartedAt = r.now().UTC()
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
		if finalizeErr := r.finalize(report, runErr); finalizeErr != nil {
			runErr = errors.Join(runErr, finalizeErr)
		}
		result := "completed"
		if runErr != nil {
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
		return report, fmt.Errorf("update ranges: %w", err)
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
	addresses, err := r.generateCandidates(rangeResult.Snapshot, stateBefore, report.StartedAt)
	if err != nil {
		return report, err
	}
	var benchmarkGuard proxy.BenchmarkGuard
	var benchmarkGuardResult proxy.BenchmarkGuardResult
	benchmarkGuardActive := false
	defer func() {
		if !benchmarkGuardActive {
			return
		}
		if cleanupErr := r.endBenchmarkGuard(ctx, benchmarkGuard, benchmarkGuardResult); cleanupErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("clean benchmark DIRECT guard: %w", cleanupErr))
		}
	}()
	if options.ApplyPolicy && r.policy != nil {
		benchmarkGuard, _ = r.policy.(proxy.BenchmarkGuard)
		if benchmarkGuard != nil {
			guardPolicy := benchmarkDirectPolicy(addresses)
			benchmarkGuardResult, err = benchmarkGuard.BeginBenchmarkGuard(ctx, guardPolicy, addresses)
			if err != nil {
				return report, fmt.Errorf("apply benchmark DIRECT guard: %w", err)
			}
			benchmarkGuardActive = len(benchmarkGuardResult.Receipts) > 0
			report.BenchmarkPath = append([]proxy.BenchmarkPathEvidence(nil), benchmarkGuardResult.Evidence...)
			for index := range report.BenchmarkPath {
				evidence := &report.BenchmarkPath[index]
				if evidence.ProxyObserved && evidence.DirectVerified {
					evidence.PhysicalRouteUsed = len(temporaryTransactions) > 0
				} else if !evidence.ProxyObserved && evidence.SocketBound && len(temporaryTransactions) > 0 {
					evidence.DirectVerified = true
					evidence.PhysicalRouteUsed = true
					evidence.Verification = "bound_socket_and_verified_physical_route"
				}
				if !evidence.DirectVerified {
					return report, fmt.Errorf("benchmark path to %s lacks DIRECT connection or verified physical-route evidence", evidence.Target)
				}
				r.logger.Info("测速直连路径验证完成", "run_id", report.ID, "adapter", evidence.Adapter, "interface", evidence.Interface, "target_ip", evidence.Target, "proxy_observed", evidence.ProxyObserved, "physical_route_used", evidence.PhysicalRouteUsed, "result", "verified")
			}
		}
	}
	r.logger.Info("候选生成完成", "run_id", report.ID, "candidates", len(addresses), "range_hash", report.RangeHash)
	r.emit(emit, Event{RunID: report.ID, Type: "stage.started", Stage: "benchmark", Message: fmt.Sprintf("testing %d candidates", len(addresses))})
	report.Results, err = r.benchmark.Run(ctx, addresses, func(progress benchmark.Progress) {
		r.emit(emit, Event{RunID: report.ID, Type: "benchmark.progress", Stage: string(progress.Stage), Progress: &progress})
	})
	if err != nil {
		return report, fmt.Errorf("benchmark candidates: %w", err)
	}
	if benchmarkGuardActive {
		if err := r.endBenchmarkGuard(ctx, benchmarkGuard, benchmarkGuardResult); err != nil {
			return report, fmt.Errorf("rollback benchmark DIRECT guard before final policy: %w", err)
		}
		benchmarkGuardActive = false
	}
	ApplyHistory(report.Results, stateBefore.Nodes)
	sort.SliceStable(report.Results, func(i, j int) bool { return report.Results[i].Score > report.Results[j].Score })
	report.IPv4Decision = Decide(report.Results, stateBefore.CurrentIPv4, 4, r.config.Benchmark, r.now())
	report.IPv6Decision = Decide(report.Results, stateBefore.CurrentIPv6, 6, r.config.Benchmark, r.now())
	r.emit(emit, Event{RunID: report.ID, Type: "selection.completed", Stage: "selection", Message: report.IPv4Decision.Reason + "; " + report.IPv6Decision.Reason})

	var applied proxy.ApplyResult
	var removedRouteTransactions []string
	if options.ApplyPolicy {
		for {
			var allocationWarnings []string
			report.domainMappings, report.DomainAllocations, allocationWarnings = r.allocateDomainMappings(ctx, rangeResult.Snapshot, report.Results, r.store.Snapshot())
			report.Warnings = append(report.Warnings, allocationWarnings...)
			report.domainAllocationCompleted = true
			applied, removedRouteTransactions, err = r.applySelectedPolicy(ctx, stateBefore, report)
			if err == nil {
				break
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
	if err := r.persistSuccessfulRun(report, stateBefore, applied, options.ApplyPolicy); err != nil {
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
	return report, nil
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
	safePolicy, err := r.policyForDecisions(before, report, false)
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
	if !r.runMutex.TryLock() {
		return ErrAlreadyRunning
	}
	defer r.runMutex.Unlock()
	return r.refreshPolicyLocked(ctx)
}

// UpdateAccelerationDomains 在单任务边界内替换域名集合，并在活动适配器可用时刷新当前策略。
func (r *Runner) UpdateAccelerationDomains(ctx context.Context, manualDomains, excludedDomains []string) (bool, error) {
	if !r.runMutex.TryLock() {
		return false, ErrAlreadyRunning
	}
	defer r.runMutex.Unlock()
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

// refreshPolicyLocked 使用已持有的单任务锁刷新策略，避免配置切换期间并行运行测速。
func (r *Runner) refreshPolicyLocked(ctx context.Context) error {
	if r.policy == nil {
		return errors.New("policy refresh requested but no adapter is configured")
	}
	rangeResult, err := r.ranges.Update(ctx, false)
	if err != nil {
		return fmt.Errorf("load ranges for policy refresh: %w", err)
	}
	for {
		before := r.store.Snapshot()
		ranked := rankedHistoricalResults(before, r.now())
		mappings, allocations, allocationWarnings := r.allocateDomainMappings(ctx, rangeResult.Snapshot, ranked, before)
		for _, warning := range allocationWarnings {
			r.logger.Warn("域名未分配优选 IP", "warning", warning)
		}
		report := RunReport{DomainAllocations: allocations, domainMappings: mappings, domainAllocationCompleted: true}
		policy, policyErr := r.policyForDecisions(before, report, false)
		if policyErr != nil {
			return policyErr
		}
		applied, applyErr := r.policy.Apply(ctx, policy)
		if applyErr != nil {
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
			if verifyErr := r.domainVerifier.VerifyApplied(ctx, policy.DomainMappings); verifyErr != nil {
				if rollbackErr := r.policy.Rollback(ctx, applied); rollbackErr != nil {
					verifyErr = errors.Join(verifyErr, rollbackErr)
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
			state.Policy = policySnapshot(policy, receipts, r.now().UTC())
			recordDomainAllocationResults(state, report.DomainAllocations, true, r.now().UTC())
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
	if err := policy.Rollback(ctx, applied); err != nil {
		return fmt.Errorf("rollback stored policy: %w", err)
	}
	return stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = nil
		state.CurrentIPv6 = nil
		state.Policy = nil
		return nil
	})
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
		route := cfnetwork.RouteSpec{Prefix: prefix.String(), Gateway: gateway, Interface: r.physicalPath.Interface, InterfaceIndex: r.physicalPath.InterfaceIndex, Metric: 5}
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
	var rollbackErrors []error
	for index := len(transactionIDs) - 1; index >= 0; index-- {
		transactionContext, cancel := context.WithTimeout(cleanupContext, r.config.Network.CommandTimeout.Duration())
		err := r.routes.Rollback(transactionContext, transactionIDs[index])
		cancel()
		if err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	return errors.Join(rollbackErrors...)
}

// applySelectedPolicy 构造节点与域名联合策略，并在应用前后分别完成 SNI 和系统映射验证。
func (r *Runner) applySelectedPolicy(ctx context.Context, state store.State, report RunReport) (proxy.ApplyResult, []string, error) {
	if r.policy == nil {
		return proxy.ApplyResult{}, nil, errors.New("policy application requested but no adapter is configured")
	}
	finalPolicy, err := r.policyForDecisions(state, report, false)
	if err != nil {
		return proxy.ApplyResult{}, nil, err
	}
	transitionPolicy, err := r.policyForDecisions(state, report, true)
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

// allocateDomainMappings 先按手动配置顺序、再按自动发现顺序消费互不重复的兼容地址。
func (r *Runner) allocateDomainMappings(ctx context.Context, snapshot ranges.Snapshot, results []benchmark.Result, state store.State) ([]proxy.DomainMapping, []DomainAllocationResult, []string) {
	if !r.config.Acceleration.Enabled {
		return nil, nil, nil
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
		return nil, nil, nil
	}
	if r.policy == nil || !r.policy.Capabilities().DomainMappings {
		allocations := failedDomainAllocations(candidatesByDomain, "domain mapping capability is unavailable")
		return nil, allocations, domainAllocationWarnings(allocations)
	}
	if r.domainResolver == nil || r.domainVerifier == nil {
		allocations := failedDomainAllocations(candidatesByDomain, "verification is unavailable")
		return nil, allocations, domainAllocationWarnings(allocations)
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
	nextCandidate := 0
	for _, candidate := range candidatesByDomain {
		allocation := DomainAllocationResult{Domain: candidate.domain, Source: candidate.source}
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
		var lastError error
		assigned := false
		for nextCandidate < len(candidates) {
			mapping := proxy.DomainMapping{Domain: candidate.domain, Addresses: []string{candidates[nextCandidate]}}
			nextCandidate++
			if err := r.domainVerifier.VerifyPreflight(ctx, []proxy.DomainMapping{mapping}); err != nil {
				lastError = err
				continue
			}
			mappings = append(mappings, mapping)
			allocation.AssignedAddress = mapping.Addresses[0]
			allocation.PreflightVerified = true
			assigned = true
			break
		}
		if assigned {
			allocations = append(allocations, allocation)
			continue
		}
		if lastError != nil {
			allocation.Error = lastError.Error()
		} else {
			allocation.Error = "ranked address pool is exhausted"
		}
		allocations = append(allocations, allocation)
	}
	return mappings, allocations, domainAllocationWarnings(allocations)
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

func (r *Runner) policyForDecisions(state store.State, report RunReport, includePrevious bool) (proxy.DirectPolicy, error) {
	capabilities := r.policy.Capabilities()
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
	if capabilities.DomainMappings {
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
		route := cfnetwork.RouteSpec{Prefix: prefix.String(), Gateway: gateway, Interface: r.physicalPath.Interface, InterfaceIndex: r.physicalPath.InterfaceIndex, Metric: 5}
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

func (r *Runner) persistSuccessfulRun(report RunReport, before store.State, applied proxy.ApplyResult, policyApplied bool) error {
	now := r.now().UTC()
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
			policy, err := r.policyForDecisions(before, report, false)
			if err != nil {
				return err
			}
			state.Policy = policySnapshot(policy, receipts, now)
		}
		recordDomainAllocationResults(state, report.DomainAllocations, policyApplied, now)
		return nil
	}); err != nil {
		return err
	}
	details, err := json.Marshal(report.Results)
	if err != nil {
		return err
	}
	return r.store.SaveRunDetail(report.ID, details, r.config.History.DetailRetention.Duration())
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
		record.CloudflareVerified = allocation.CloudflareVerified
		record.PreflightVerified = allocation.PreflightVerified
		record.Active = policyApplied && allocation.AssignedAddress != ""
		record.LastResolvedAddresses = append([]string(nil), allocation.ResolvedAddresses...)
		record.LastError = allocation.Error
		if record.Active {
			record.LastError = ""
		}
		state.DiscoveredDomains[allocation.Domain] = record
	}
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
		if runErr != nil {
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
		if runErr != nil {
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
