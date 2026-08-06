package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/benchmark"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/diagnostics"
	"github.com/cf-optimizer/cf-optimizer/internal/ipc"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/optimizer"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/guard"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
	"github.com/cf-optimizer/cf-optimizer/internal/version"
)

// API 将运行时能力暴露为经过参数校验的本地 IPC 方法。
type API struct {
	runtime               *Runtime
	activeMutex           sync.Mutex
	activeCancel          context.CancelFunc
	activeEvent           *optimizer.Event
	startupMutex          sync.RWMutex
	startupStatus         StartupStatus
	scheduleMutex         sync.RWMutex
	scheduleStatus        ScheduleStatus
	policyGuardMutex      sync.RWMutex
	policyGuards          map[string]guard.Status
	configurationMutex    sync.Mutex
	quickStartMutex       sync.Mutex
	quickStartPlan        *quickStartPlanRecord
	discoverPhysicalPath  physicalPathDiscoverer
	networkFingerprint    networkFingerprinter
	detectManagedAdapters managedAdapterDetector
	buildManagedSession   managedSessionBuilder
	saveConfig            func(string, config.Config) error
	reloadConfig          func(context.Context, config.Config, bool) (bool, error)
	now                   func() time.Time
}

// ScheduleStatus 描述调度器当前承诺的下一次执行时间，不包含内部计时器细节。
type ScheduleStatus struct {
	Enabled         bool       `json:"enabled"`
	Interval        string     `json:"interval"`
	NextScheduledAt *time.Time `json:"next_scheduled_at,omitempty"`
	Trigger         string     `json:"trigger,omitempty"`
}

// StartupStatus 描述后台服务是否已完成启动时的路由和策略恢复。
type StartupStatus struct {
	Ready     bool             `json:"ready"`
	Stage     string           `json:"stage,omitempty"`
	Message   string           `json:"message,omitempty"`
	Progress  *StartupProgress `json:"progress,omitempty"`
	StartedAt time.Time        `json:"started_at,omitempty"`
	UpdatedAt time.Time        `json:"updated_at,omitempty"`
}

// StartupProgress 描述当前恢复阶段已经处理和需要处理的事务数量。
type StartupProgress struct {
	Completed int `json:"completed"`
	Total     int `json:"total"`
}

// LatestBenchmark 返回最近一次已持久化成功任务中供界面展示的前 N 个候选结果。
type LatestBenchmark struct {
	RunID      string             `json:"run_id"`
	FinishedAt time.Time          `json:"finished_at"`
	SavedAt    time.Time          `json:"saved_at"`
	Results    []benchmark.Result `json:"results"`
}

// statusState 仅包含普通状态轮询所需字段，避免通过 IPC 暴露节点明细和策略回滚收据。
type statusState struct {
	Version       int              `json:"version"`
	UpdatedAt     time.Time        `json:"updated_at"`
	CurrentIPv4   *store.Selection `json:"current_ipv4,omitempty"`
	CurrentIPv6   *store.Selection `json:"current_ipv6,omitempty"`
	LastError     string           `json:"last_error,omitempty"`
	LastStartedAt time.Time        `json:"last_started_at,omitempty"`
	LastEndedAt   time.Time        `json:"last_ended_at,omitempty"`
	Running       bool             `json:"running"`
}

// NewAPI 创建后台服务业务处理器。
func NewAPI(runtime *Runtime) (*API, error) {
	if runtime == nil {
		return nil, errors.New("application runtime is required")
	}
	schedule := runtime.View().Config.Schedule
	return &API{
		runtime: runtime, discoverPhysicalPath: cfnetwork.DiscoverPhysicalPath,
		scheduleStatus:        ScheduleStatus{Enabled: schedule.Enabled, Interval: schedule.Interval.String()},
		startupStatus:         StartupStatus{Ready: true, Stage: "ready", StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
		policyGuards:          map[string]guard.Status{},
		networkFingerprint:    cfnetwork.NetworkFingerprint,
		detectManagedAdapters: runtime.DetectManagedAdapters,
		buildManagedSession:   runtime.BuildManagedSession,
		saveConfig:            config.Save,
		reloadConfig:          runtime.ReloadConfig,
		now:                   time.Now,
	}, nil
}

// Handle 路由白名单方法，并在每个边界严格解码参数。
func (a *API) Handle(ctx context.Context, request ipc.Request, emit func(any) error) (any, error) {
	if err := a.rejectDuringStartup(request.Method); err != nil {
		return nil, err
	}
	switch request.Method {
	case "system.status":
		return a.systemStatus(), nil
	case "optimizer.run":
		return a.runOptimizer(ctx, request.Params, emit)
	case "optimizer.cancel":
		return a.cancelOptimizer()
	case "quickstart.plan":
		return a.planQuickStart(ctx, request.Params)
	case "quickstart.run":
		return a.runQuickStart(ctx, request.Params, emit)
	case "ranges.get":
		return a.runtime.View().Ranges.Load()
	case "ranges.update":
		return a.runtime.View().Ranges.Update(ctx, true)
	case "history.list":
		return newestHistoryFirst(a.runtime.Store.Snapshot().History), nil
	case "history.latest":
		return a.latestBenchmark(request.Params)
	case "routes.list":
		return a.runtime.View().Routes.Transactions(), nil
	case "proxy.detect":
		return a.runtime.DetectProxyAdapters(ctx), nil
	case "acceleration.domains":
		return a.runtime.domainDiscoverySnapshot(), nil
	case "acceleration.discover":
		return a.runtime.DiscoverAccelerationDomains(ctx)
	case "acceleration.clear_discovered":
		var parameters struct{}
		if err := decodeStrict(request.Params, &parameters); err != nil {
			return nil, invalidParams(err)
		}
		return a.runtime.ClearDiscoveredAccelerationDomains(ctx)
	case "acceleration.domain_test":
		var parameters struct {
			Domain  string `json:"domain"`
			Address string `json:"address"`
		}
		if err := decodeStrict(request.Params, &parameters); err != nil {
			return nil, invalidParams(err)
		}
		if strings.TrimSpace(parameters.Domain) == "" || strings.TrimSpace(parameters.Address) == "" {
			return nil, invalidParams(errors.New("domain and address are required"))
		}
		return a.runtime.TestManualDomain(ctx, parameters.Domain, parameters.Address)
	case "acceleration.domain_apply":
		var parameters struct {
			Domain  string `json:"domain"`
			Address string `json:"address"`
		}
		if err := decodeStrict(request.Params, &parameters); err != nil {
			return nil, invalidParams(err)
		}
		if strings.TrimSpace(parameters.Domain) == "" || strings.TrimSpace(parameters.Address) == "" {
			return nil, invalidParams(errors.New("domain and address are required"))
		}
		return a.runtime.ApplyManualDomainMapping(ctx, parameters.Domain, parameters.Address)
	case "diagnostics.route":
		return a.routeDiagnostics(ctx, request.Params)
	case "config.get":
		return a.desiredConfig()
	case "config.update":
		return a.updateConfig(ctx, request.Params)
	case "logs.tail":
		return a.tailLogs(request.Params)
	default:
		return nil, &ipc.Error{Code: "method_not_found", Message: "unknown method " + request.Method}
	}
}

// SetStartupStatus 更新服务启动恢复阶段，状态轮询可在 IPC 监听后立即读取。
func (a *API) SetStartupStatus(status StartupStatus) {
	now := time.Now().UTC()
	if status.StartedAt.IsZero() {
		status.StartedAt = now
	}
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = now
	}
	a.startupMutex.Lock()
	a.startupStatus = cloneStartupStatus(status)
	a.startupMutex.Unlock()
}

// startupSnapshot 返回启动状态副本，并兼容绕过 NewAPI 构造的单元测试。
func (a *API) startupSnapshot() StartupStatus {
	a.startupMutex.RLock()
	status := cloneStartupStatus(a.startupStatus)
	a.startupMutex.RUnlock()
	if status.UpdatedAt.IsZero() {
		status = StartupStatus{Ready: true, Stage: "ready"}
	}
	return status
}

func cloneStartupStatus(status StartupStatus) StartupStatus {
	// Progress 指针单独复制，避免状态轮询拿到仍会被后台更新的内部对象。
	if status.Progress != nil {
		progress := *status.Progress
		status.Progress = &progress
	}
	return status
}

// rejectDuringStartup 防止恢复事务未完成时执行新的路由、代理或配置修改。
func (a *API) rejectDuringStartup(method string) error {
	status := a.startupSnapshot()
	if status.Ready || startupReadOnlyMethod(method) {
		return nil
	}
	message := status.Message
	if message == "" {
		message = "后台服务正在恢复中，请稍后重试"
	}
	return &ipc.Error{Code: "service_initializing", Message: message}
}

// startupReadOnlyMethod 返回恢复期间仍可安全执行的只读 IPC 方法。
func startupReadOnlyMethod(method string) bool {
	switch method {
	case "system.status", "logs.tail", "history.list", "history.latest", "routes.list", "config.get", "ranges.get", "proxy.detect", "acceleration.domains", "diagnostics.route":
		return true
	default:
		return false
	}
}

// latestBenchmark 从新到旧寻找已有明细的成功任务；失败任务不会覆盖上一份可展示结果。
func (a *API) latestBenchmark(raw json.RawMessage) (LatestBenchmark, error) {
	var parameters struct{}
	if err := decodeStrict(raw, &parameters); err != nil {
		return LatestBenchmark{}, invalidParams(err)
	}
	history := a.runtime.Store.Snapshot().History
	for index := len(history) - 1; index >= 0; index-- {
		summary := history[index]
		detail, err := a.runtime.Store.LoadRunDetail(summary.ID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return LatestBenchmark{}, fmt.Errorf("load latest benchmark %s: %w", summary.ID, err)
		}
		var results []benchmark.Result
		if err := json.Unmarshal(detail.Payload, &results); err != nil {
			return LatestBenchmark{}, fmt.Errorf("decode latest benchmark %s: %w", summary.ID, err)
		}
		if results == nil {
			results = []benchmark.Result{}
		}
		results = trimBenchmarkResults(results, benchmarkDisplayLimit(a.runtime.View().Config))
		return LatestBenchmark{RunID: summary.ID, FinishedAt: summary.FinishedAt, SavedAt: detail.SavedAt, Results: results}, nil
	}
	return LatestBenchmark{Results: []benchmark.Result{}}, nil
}

// newestHistoryFirst 返回供界面扫描的倒序历史副本，不改变持久化追加顺序。
func newestHistoryFirst(history []store.RunSummary) []store.RunSummary {
	result := slices.Clone(history)
	slices.Reverse(result)
	return result
}

// systemStatus 返回可高频轮询的精简状态，历史和领域明细由各自的只读接口提供。
func (a *API) systemStatus() map[string]any {
	a.activeMutex.Lock()
	activeEvent := cloneEvent(a.activeEvent)
	a.activeMutex.Unlock()
	a.scheduleMutex.RLock()
	scheduleStatus := cloneScheduleStatus(a.scheduleStatus)
	a.scheduleMutex.RUnlock()
	a.policyGuardMutex.RLock()
	policyGuards := clonePolicyGuardStatuses(a.policyGuards)
	a.policyGuardMutex.RUnlock()
	view := a.runtime.View()
	state := a.runtime.Store.Snapshot()
	startupStatus := a.startupSnapshot()
	return map[string]any{
		"build": version.Metadata(), "protocol_version": ipc.ProtocolVersion,
		"state": statusState{
			Version: state.Version, UpdatedAt: state.UpdatedAt,
			CurrentIPv4: state.CurrentIPv4, CurrentIPv6: state.CurrentIPv6,
			LastError: state.LastError, LastStartedAt: state.LastStartedAt,
			LastEndedAt: state.LastEndedAt, Running: state.Running,
		},
		"physical_path":    view.PhysicalPath,
		"policy_available": view.ProxyCoordinator != nil,
		"active_event":     activeEvent,
		"schedule":         scheduleStatus,
		"policy_guards":    policyGuards,
		"startup":          startupStatus,
	}
}

// SetPolicyGuardStatus 更新一个内核策略的去敏守护状态。
func (a *API) SetPolicyGuardStatus(status guard.Status) {
	a.policyGuardMutex.Lock()
	if a.policyGuards == nil {
		a.policyGuards = map[string]guard.Status{}
	}
	a.policyGuards[status.ID] = clonePolicyGuardStatus(status)
	a.policyGuardMutex.Unlock()
}

// ResetPolicyGuardStatuses 清除配置切换前的旧策略实例状态。
func (a *API) ResetPolicyGuardStatuses() {
	a.policyGuardMutex.Lock()
	a.policyGuards = map[string]guard.Status{}
	a.policyGuardMutex.Unlock()
}

func clonePolicyGuardStatuses(statuses map[string]guard.Status) map[string]guard.Status {
	result := make(map[string]guard.Status, len(statuses))
	for id, status := range statuses {
		result[id] = clonePolicyGuardStatus(status)
	}
	return result
}

func clonePolicyGuardStatus(status guard.Status) guard.Status {
	status.DriftReasons = append([]string(nil), status.DriftReasons...)
	cloneTime := func(value *time.Time) *time.Time {
		if value == nil {
			return nil
		}
		copy := *value
		return &copy
	}
	status.LastCheckedAt = cloneTime(status.LastCheckedAt)
	status.LastVerifiedAt = cloneTime(status.LastVerifiedAt)
	status.RetryAt = cloneTime(status.RetryAt)
	return status
}

// SetScheduleStatus 原子更新供 system.status 读取的调度承诺。
func (a *API) SetScheduleStatus(status ScheduleStatus) {
	a.scheduleMutex.Lock()
	a.scheduleStatus = cloneScheduleStatus(status)
	a.scheduleMutex.Unlock()
}

// cloneScheduleStatus 隔离时间指针，避免调度器与状态轮询共享可变数据。
func cloneScheduleStatus(status ScheduleStatus) ScheduleStatus {
	if status.NextScheduledAt != nil {
		next := *status.NextScheduledAt
		status.NextScheduledAt = &next
	}
	return status
}

func (a *API) runOptimizer(ctx context.Context, raw json.RawMessage, emit func(any) error) (optimizer.RunReport, error) {
	var parameters optimizer.RunOptions
	if err := decodeStrict(raw, &parameters); err != nil {
		return optimizer.RunReport{}, invalidParams(err)
	}
	return a.RunOptimization(ctx, parameters, func(event optimizer.Event) error { return emit(event) })
}

// RunOptimization 统一管理 IPC 与调度器发起任务的取消句柄。
func (a *API) RunOptimization(ctx context.Context, parameters optimizer.RunOptions, emit func(optimizer.Event) error) (optimizer.RunReport, error) {
	return a.runWithRunner(ctx, a.runtime.View().Runner, parameters, emit)
}

// runWithRunner 让普通任务和快速流程共享同一个可取消单任务边界。
func (a *API) runWithRunner(ctx context.Context, runner *optimizer.Runner, parameters optimizer.RunOptions, emit func(optimizer.Event) error) (optimizer.RunReport, error) {
	if runner == nil {
		return optimizer.RunReport{}, errors.New("optimizer runner is unavailable")
	}
	runContext, cancel := context.WithCancel(ctx)
	if !a.setActiveCancel(cancel) {
		cancel()
		return optimizer.RunReport{}, &ipc.Error{Code: "conflict", Message: optimizer.ErrAlreadyRunning.Error()}
	}
	defer func() {
		cancel()
		a.clearActiveCancel()
	}()
	var emitMutex sync.Mutex
	streamConnected := true
	report, err := runner.Run(runContext, parameters, func(event optimizer.Event) {
		a.setActiveEvent(event)
		emitMutex.Lock()
		defer emitMutex.Unlock()
		if !streamConnected || emit == nil {
			return
		}
		if eventErr := emit(event); eventErr != nil {
			streamConnected = false
			a.runtime.Logger.Warn("任务事件订阅已断开，后台任务继续运行", "component", "ipc", "run_id", event.RunID, "error", eventErr)
		}
	})
	if errors.Is(err, optimizer.ErrAlreadyRunning) {
		return report, &ipc.Error{Code: "conflict", Message: err.Error()}
	}
	if errors.Is(err, context.Canceled) {
		return report, &ipc.Error{Code: "cancelled", Message: "optimization was cancelled"}
	}
	report.Results = trimBenchmarkResults(report.Results, benchmarkDisplayLimit(a.runtime.View().Config))
	return report, err
}

// benchmarkDisplayLimit 返回界面展示和 IPC 返回使用的前 N 名数量。
func benchmarkDisplayLimit(cfg config.Config) int {
	if cfg.Benchmark.DownloadTop > 0 {
		return cfg.Benchmark.DownloadTop
	}
	return config.Default().Benchmark.DownloadTop
}

// trimBenchmarkResults 截取已按评分排序的测速结果，保留持久化明细不变。
func trimBenchmarkResults(results []benchmark.Result, limit int) []benchmark.Result {
	if len(results) <= limit {
		return results
	}
	return slices.Clone(results[:limit])
}

func (a *API) cancelOptimizer() (map[string]bool, error) {
	a.activeMutex.Lock()
	defer a.activeMutex.Unlock()
	if a.activeCancel == nil {
		return map[string]bool{"cancelled": false}, nil
	}
	a.activeCancel()
	return map[string]bool{"cancelled": true}, nil
}

func (a *API) setActiveCancel(cancel context.CancelFunc) bool {
	a.activeMutex.Lock()
	defer a.activeMutex.Unlock()
	if a.activeCancel != nil {
		return false
	}
	a.activeCancel = cancel
	return true
}

func (a *API) clearActiveCancel() {
	a.activeMutex.Lock()
	a.activeCancel = nil
	a.activeEvent = nil
	a.activeMutex.Unlock()
}

func (a *API) setActiveEvent(event optimizer.Event) {
	a.activeMutex.Lock()
	a.activeEvent = cloneEvent(&event)
	a.activeMutex.Unlock()
}

func cloneEvent(event *optimizer.Event) *optimizer.Event {
	if event == nil {
		return nil
	}
	clone := *event
	if event.Progress != nil {
		progress := *event.Progress
		clone.Progress = &progress
	}
	return &clone
}

func (a *API) routeDiagnostics(ctx context.Context, raw json.RawMessage) (diagnostics.Report, error) {
	var parameters struct {
		Target string `json:"target"`
	}
	if err := decodeStrict(raw, &parameters); err != nil {
		return diagnostics.Report{}, invalidParams(err)
	}
	target, err := netip.ParseAddr(parameters.Target)
	if err != nil || !target.IsGlobalUnicast() {
		return diagnostics.Report{}, invalidParams(errors.New("target must be a global unicast IP address"))
	}
	view := a.runtime.View()
	return diagnostics.Generate(ctx, target, view.PhysicalPath, view.RouteBackend, view.DirectDial, view.Config.Network.CommandTimeout.Duration()), nil
}

func (a *API) updateConfig(ctx context.Context, raw json.RawMessage) (map[string]bool, error) {
	var parameters struct {
		Config config.Config `json:"config"`
	}
	if err := decodeStrict(raw, &parameters); err != nil {
		return nil, invalidParams(err)
	}
	if !a.configurationMutex.TryLock() {
		return nil, &ipc.Error{Code: "conflict", Message: "configuration update or quick-start run is already active"}
	}
	defer a.configurationMutex.Unlock()
	view := a.runtime.View()
	parameters.Config.Proxy.Mihomo.Secret = view.Config.Proxy.Mihomo.Secret
	parameters.Config.ApplyDefaults()
	if parameters.Config.DataDir == "" {
		parameters.Config.DataDir = view.Config.DataDir
	}
	if err := parameters.Config.Validate(); err != nil {
		return nil, invalidParams(err)
	}
	if a.runtime.ConfigPath == "" {
		return nil, &ipc.Error{Code: "precondition_failed", Message: "daemon has no configured config file path"}
	}
	persistedBefore, err := a.desiredConfig()
	if err != nil {
		return nil, fmt.Errorf("load persisted config before update: %w", err)
	}
	policyRefreshRequired := policyRuntimeConfigChanged(view.Config, parameters.Config)
	restartRequired := parameters.Config.DataDir != view.Config.DataDir || parameters.Config.IPC.Endpoint != view.Config.IPC.Endpoint
	reloadRequired := !configsEqual(view.Config, parameters.Config)

	updateContext := ctx
	if reloadRequired && !restartRequired {
		var cancel context.CancelFunc
		updateContext, cancel = context.WithCancel(ctx)
		if !a.setActiveCancel(cancel) {
			cancel()
			return nil, &ipc.Error{Code: "conflict", Message: optimizer.ErrAlreadyRunning.Error()}
		}
		a.setActiveEvent(optimizer.Event{
			RunID:     fmt.Sprintf("config-update-%d", a.now().UTC().UnixNano()),
			Type:      "stage.started",
			Stage:     "config",
			Message:   "updating configuration and refreshing verified policy",
			Timestamp: a.now().UTC(),
		})
		defer func() {
			cancel()
			a.clearActiveCancel()
		}()
	}
	if err := a.saveConfig(a.runtime.ConfigPath, parameters.Config); err != nil {
		return nil, err
	}
	policyRefreshed := false
	hotApplied := false
	if reloadRequired && !restartRequired {
		var reloadErr error
		policyRefreshed, reloadErr = a.reloadConfig(updateContext, parameters.Config, policyRefreshRequired)
		if reloadErr != nil {
			restoreErr := a.saveConfig(a.runtime.ConfigPath, persistedBefore)
			if a.runtime.Logger != nil {
				result := "rolled_back"
				if restoreErr != nil {
					result = "rollback_failed"
				}
				a.runtime.Logger.Warn("运行配置热重载失败", "component", "config", "rollback_succeeded", restoreErr == nil, "result", result, "error", reloadErr)
			}
			if restoreErr == nil && errors.Is(reloadErr, optimizer.ErrAlreadyRunning) {
				return nil, &ipc.Error{Code: "conflict", Message: optimizer.ErrAlreadyRunning.Error()}
			}
			if restoreErr != nil {
				reloadErr = errors.Join(reloadErr, fmt.Errorf("restore persisted config: %w", restoreErr))
			}
			return nil, reloadErr
		}
		hotApplied = true
	}
	if a.runtime.Logger != nil {
		a.runtime.Logger.Info("配置保存完成", "component", "config", "manual_domains", len(parameters.Config.Acceleration.ManualDomains), "excluded_domains", len(parameters.Config.Acceleration.ExcludedDomains), "hot_applied", hotApplied, "policy_refreshed", policyRefreshed, "restart_required", restartRequired, "result", "completed")
	}
	return map[string]bool{"saved": true, "hot_applied": hotApplied, "policy_refreshed": policyRefreshed, "restart_required": restartRequired}, nil
}

// configsEqual 比较保存目标与当前运行配置，忽略切片底层数组差异。
func configsEqual(left, right config.Config) bool {
	return valuesEqual(left, right)
}

// policyRuntimeConfigChanged 判断是否需要用新物理路径和适配器重新验证当前策略。
func policyRuntimeConfigChanged(left, right config.Config) bool {
	return !valuesEqual(left.Network, right.Network) ||
		!valuesEqual(left.Proxy, right.Proxy) ||
		!valuesEqual(left.Hosts, right.Hosts) ||
		!valuesEqual(left.Acceleration, right.Acceleration)
}

// valuesEqual 使用稳定 JSON 表达比较仅包含配置字段的值。
func valuesEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

// desiredConfig 返回磁盘中的期望配置；配置文件尚未建立时回退到当前运行时快照。
func (a *API) desiredConfig() (config.Config, error) {
	view := a.runtime.View()
	if a.runtime.ConfigPath == "" {
		return view.Config, nil
	}
	if _, err := os.Stat(a.runtime.ConfigPath); errors.Is(err, os.ErrNotExist) {
		return view.Config, nil
	} else if err != nil {
		return config.Config{}, err
	}
	persisted, err := config.Load(a.runtime.ConfigPath, view.Config.DataDir)
	if err != nil {
		return config.Config{}, err
	}
	return mergeDetectedMihomoConfig(persisted, view.Config, view.MihomoAutoDetected), nil
}

func (a *API) tailLogs(raw json.RawMessage) ([]string, error) {
	parameters := struct {
		Lines int `json:"lines"`
	}{Lines: 200}
	if err := decodeStrict(raw, &parameters); err != nil {
		return nil, invalidParams(err)
	}
	if parameters.Lines < 1 || parameters.Lines > 2000 {
		return nil, invalidParams(errors.New("lines must be between 1 and 2000"))
	}
	content, err := os.ReadFile(filepath.Join(a.runtime.View().Config.DataDir, "logs", "cf-optimizer.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) > parameters.Lines {
		lines = lines[len(lines)-parameters.Lines:]
	}
	return lines, nil
}

func decodeStrict(raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func invalidParams(err error) *ipc.Error {
	return &ipc.Error{Code: "invalid_params", Message: fmt.Sprintf("invalid parameters: %v", err)}
}
