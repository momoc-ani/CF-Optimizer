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
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

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

// RunReport 汇总候选结果、地址族决策、策略状态和可恢复警告。
type RunReport struct {
	ID            string             `json:"id"`
	StartedAt     time.Time          `json:"started_at"`
	FinishedAt    time.Time          `json:"finished_at"`
	RangeSource   string             `json:"range_source"`
	RangeHash     string             `json:"range_hash"`
	Results       []benchmark.Result `json:"results"`
	IPv4Decision  Decision           `json:"ipv4_decision"`
	IPv6Decision  Decision           `json:"ipv6_decision"`
	PolicyApplied bool               `json:"policy_applied"`
	Warnings      []string           `json:"warnings,omitempty"`
}

// Runner 协调网段、候选、测速、稳定选择、路由和代理策略的完整事务。
type Runner struct {
	config       config.Config
	ranges       RangeSource
	benchmark    Benchmarker
	store        *store.Store
	routes       *cfnetwork.RouteController
	physicalPath cfnetwork.PhysicalPath
	policy       PolicyApplier
	logger       *slog.Logger
	now          func() time.Time
	runMutex     sync.Mutex
}

// NewRunner 创建依赖显式注入的优选运行器。
func NewRunner(cfg config.Config, rangeSource RangeSource, benchmarker Benchmarker, stateStore *store.Store, routes *cfnetwork.RouteController, physicalPath cfnetwork.PhysicalPath, policy PolicyApplier, logger *slog.Logger) (*Runner, error) {
	if rangeSource == nil || benchmarker == nil || stateStore == nil || logger == nil {
		return nil, errors.New("range source, benchmarker, store and logger are required")
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

	temporaryTransactions, err := r.applyTemporaryRoutes(ctx, rangeResult.Snapshot, options.ApplyPolicy)
	if err != nil {
		return report, err
	}
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.config.Network.CommandTimeout.Duration())
		defer cancel()
		if cleanupErr := r.rollbackRoutes(cleanupContext, temporaryTransactions); cleanupErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("clean temporary routes: %w", cleanupErr))
		}
	}()

	stateBefore := r.store.Snapshot()
	addresses, err := r.generateCandidates(rangeResult.Snapshot, stateBefore, report.StartedAt)
	if err != nil {
		return report, err
	}
	r.logger.Info("候选生成完成", "run_id", report.ID, "candidates", len(addresses), "range_hash", report.RangeHash)
	r.emit(emit, Event{RunID: report.ID, Type: "stage.started", Stage: "benchmark", Message: fmt.Sprintf("testing %d candidates", len(addresses))})
	report.Results, err = r.benchmark.Run(ctx, addresses, func(progress benchmark.Progress) {
		r.emit(emit, Event{RunID: report.ID, Type: "benchmark.progress", Stage: string(progress.Stage), Progress: &progress})
	})
	if err != nil {
		return report, fmt.Errorf("benchmark candidates: %w", err)
	}
	ApplyHistory(report.Results, stateBefore.Nodes)
	sort.SliceStable(report.Results, func(i, j int) bool { return report.Results[i].Score > report.Results[j].Score })
	report.IPv4Decision = Decide(report.Results, stateBefore.CurrentIPv4, 4, r.config.Benchmark, r.now())
	report.IPv6Decision = Decide(report.Results, stateBefore.CurrentIPv6, 6, r.config.Benchmark, r.now())
	r.emit(emit, Event{RunID: report.ID, Type: "selection.completed", Stage: "selection", Message: report.IPv4Decision.Reason + "; " + report.IPv6Decision.Reason})

	var applied proxy.ApplyResult
	if options.ApplyPolicy {
		applied, err = r.applySelectedPolicy(ctx, stateBefore, report)
		if err != nil {
			return report, err
		}
		report.PolicyApplied = true
		if cleanupErr := r.removeReplacedHostRoutes(ctx, stateBefore, report); cleanupErr != nil {
			report.Warnings = append(report.Warnings, cleanupErr.Error())
		}
	}
	if err := r.persistSuccessfulRun(report, stateBefore, applied, options.ApplyPolicy); err != nil {
		return report, err
	}
	return report, nil
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

func (r *Runner) rollbackRoutes(ctx context.Context, transactionIDs []string) error {
	if r.routes == nil {
		return nil
	}
	var rollbackErrors []error
	for index := len(transactionIDs) - 1; index >= 0; index-- {
		if err := r.routes.Rollback(ctx, transactionIDs[index]); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	return errors.Join(rollbackErrors...)
}

func (r *Runner) applySelectedPolicy(ctx context.Context, state store.State, report RunReport) (proxy.ApplyResult, error) {
	if r.policy == nil {
		return proxy.ApplyResult{}, errors.New("policy application requested but no adapter is configured")
	}
	finalPolicy, err := r.policyForDecisions(state, report, false)
	if err != nil {
		return proxy.ApplyResult{}, err
	}
	transitionPolicy, err := r.policyForDecisions(state, report, true)
	if err != nil {
		return proxy.ApplyResult{}, err
	}
	var transition proxy.ApplyResult
	if policiesDiffer(finalPolicy, transitionPolicy) {
		transition, err = r.policy.Apply(ctx, transitionPolicy)
		if err != nil {
			return proxy.ApplyResult{}, fmt.Errorf("apply transition policy: %w", err)
		}
	}
	finalResult, err := r.policy.Apply(ctx, finalPolicy)
	if err != nil {
		if rollbackErr := r.policy.Rollback(ctx, transition); rollbackErr != nil {
			err = errors.Join(err, rollbackErr)
		}
		return proxy.ApplyResult{}, fmt.Errorf("apply final policy: %w", err)
	}
	return finalResult, nil
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
		if state.CurrentIPv4 != nil {
			appendAddress(state.CurrentIPv4.IP)
		}
		if state.CurrentIPv6 != nil {
			appendAddress(state.CurrentIPv6.IP)
		}
	}
	if capabilities.Domains {
		policy.Domains = append(policy.Domains, r.config.Hosts.Domains...)
		if serverName := strings.TrimSpace(r.config.Benchmark.TLSServerName); serverName != "" {
			policy.Domains = append(policy.Domains, serverName)
		}
		if r.config.Benchmark.DownloadURL != "" {
			if parsed, err := url.Parse(r.config.Benchmark.DownloadURL); err == nil && parsed.Hostname() != "" {
				policy.Domains = append(policy.Domains, parsed.Hostname())
			}
		}
	}
	if len(policy.IPv4CIDRs)+len(policy.IPv6CIDRs) == 0 {
		return proxy.DirectPolicy{}, errors.New("no qualified or current IP is available for policy application")
	}
	normalized, err := policy.Normalize()
	if err != nil {
		return proxy.DirectPolicy{}, err
	}
	return normalized, nil
}

func policiesDiffer(left, right proxy.DirectPolicy) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) != string(rightJSON)
}

func (r *Runner) removeReplacedHostRoutes(ctx context.Context, state store.State, report RunReport) error {
	if !r.config.Network.ManageRoutes || r.routes == nil {
		return nil
	}
	var removalErrors []error
	items := []struct {
		current  *store.Selection
		decision Decision
	}{
		{state.CurrentIPv4, report.IPv4Decision},
		{state.CurrentIPv6, report.IPv6Decision},
	}
	for _, item := range items {
		if item.current == nil || !item.decision.ShouldSwitch || item.current.IP == item.decision.Selected.IP.String() {
			continue
		}
		address, err := netip.ParseAddr(item.current.IP)
		if err != nil {
			continue
		}
		bits := 128
		gateway := r.physicalPath.GatewayIPv6
		if address.Is4() {
			bits = 32
			gateway = r.physicalPath.GatewayIPv4
		}
		route := cfnetwork.RouteSpec{Prefix: netip.PrefixFrom(address, bits).String(), Gateway: gateway, Interface: r.physicalPath.Interface, InterfaceIndex: r.physicalPath.InterfaceIndex, Metric: 5}
		if _, err := r.routes.Remove(ctx, route); err != nil {
			removalErrors = append(removalErrors, err)
		}
	}
	return errors.Join(removalErrors...)
}

func (r *Runner) persistSuccessfulRun(report RunReport, before store.State, applied proxy.ApplyResult, policyApplied bool) error {
	now := r.now().UTC()
	if err := r.store.Update(func(state *store.State) error {
		RecordResults(state.Nodes, report.Results, r.config.Benchmark, now)
		if policyApplied {
			state.CurrentIPv4 = updateSelection(before.CurrentIPv4, report.IPv4Decision, now)
			state.CurrentIPv6 = updateSelection(before.CurrentIPv6, report.IPv6Decision, now)
			receipts, err := json.Marshal(applied)
			if err != nil {
				return err
			}
			policy, err := r.policyForDecisions(before, report, false)
			if err != nil {
				return err
			}
			state.Policy = &store.PolicySnapshot{
				IPv4CIDRs: policy.IPv4CIDRs, IPv6CIDRs: policy.IPv6CIDRs, Domains: policy.Domains,
				Processes: policy.Processes, Receipts: receipts, AppliedAt: now,
			}
		}
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
