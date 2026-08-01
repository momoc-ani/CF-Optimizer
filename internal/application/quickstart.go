package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/ipc"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/optimizer"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"gopkg.in/yaml.v3"
)

const (
	quickStartPlanTTL          = 5 * time.Minute
	quickStartApplyOnce        = "apply_once"
	quickStartApplyAndRemember = "apply_and_remember"
)

type physicalPathDiscoverer func(context.Context, string, string, string, time.Duration) (cfnetwork.PhysicalPath, error)
type networkFingerprinter func(context.Context, time.Duration) (string, error)
type managedAdapterDetector func(context.Context, cfnetwork.PhysicalPath) (map[string]proxy.Detection, error)
type managedSessionBuilder func(cfnetwork.PhysicalPath, map[string]proxy.Detection) (RuntimeSession, error)

// QuickStartPlan 是不产生系统修改、且必须由后续确认请求原样引用的短期计划。
type QuickStartPlan struct {
	PlanID                 string                     `json:"plan_id"`
	ExpiresAt              time.Time                  `json:"expires_at"`
	PhysicalPath           cfnetwork.PhysicalPath     `json:"physical_path"`
	Effects                []string                   `json:"effects"`
	Warnings               []string                   `json:"warnings,omitempty"`
	Detections             map[string]proxy.Detection `json:"detections"`
	CanApply               bool                       `json:"can_apply"`
	ManualRequired         bool                       `json:"manual_required"`
	AutoMaintenanceEnabled bool                       `json:"auto_maintenance_enabled"`
}

type quickStartPlanRecord struct {
	plan               QuickStartPlan
	networkFingerprint string
	configSummary      string
	detectedAdapters   []string
}

// QuickStartResult 汇总快速流程的验证状态，避免把写入动作误报为直连成功。
type QuickStartResult struct {
	Report                 optimizer.RunReport `json:"report"`
	Mode                   string              `json:"mode"`
	Status                 string              `json:"status"`
	AutoMaintenanceEnabled bool                `json:"auto_maintenance_enabled"`
	PersistenceWarning     string              `json:"persistence_warning,omitempty"`
	Error                  string              `json:"error,omitempty"`
}

// planQuickStart 发现物理出口和适配器，仅缓存指纹绑定的只读确认计划。
func (a *API) planQuickStart(ctx context.Context, raw json.RawMessage) (QuickStartPlan, error) {
	var parameters struct{}
	if err := decodeStrict(raw, &parameters); err != nil {
		return QuickStartPlan{}, invalidParams(err)
	}
	view := a.runtime.View()
	plan := QuickStartPlan{
		PlanID: newQuickStartPlanID(), ExpiresAt: a.now().UTC().Add(quickStartPlanTTL),
		Detections: map[string]proxy.Detection{}, AutoMaintenanceEnabled: view.Config.Network.ManageRoutes,
	}
	configSummary, hasPendingConfig, err := a.currentConfigSummary()
	if err != nil {
		plan.Warnings = append(plan.Warnings, "无法确认当前配置状态："+err.Error())
	}
	if hasPendingConfig {
		plan.Warnings = append(plan.Warnings, "配置文件包含尚未由后台服务加载的更改，请先重启后台服务")
	}

	path, pathErr := a.discoverPhysicalPath(
		ctx, view.Config.Network.Interface, view.Config.Network.GatewayIPv4,
		view.Config.Network.GatewayIPv6, view.Config.Network.CommandTimeout.Duration(),
	)
	plan.PhysicalPath = path
	if pathErr != nil {
		plan.Warnings = append(plan.Warnings, "自动发现物理出口失败："+pathErr.Error())
	}
	fingerprint, fingerprintErr := a.networkFingerprint(ctx, view.Config.Network.CommandTimeout.Duration())
	if fingerprintErr != nil {
		plan.Warnings = append(plan.Warnings, "无法生成网络指纹："+fingerprintErr.Error())
	}
	pathValid := validateManagedPath(view.Config, path) == nil
	if pathErr == nil && pathValid {
		detections, detectErr := a.detectManagedAdapters(ctx, path)
		if detectErr != nil {
			plan.Warnings = append(plan.Warnings, "适配器预检失败："+detectErr.Error())
		} else {
			plan.Detections = detections
			plan.Effects = quickStartEffects(view.Config, detections)
			plan.Warnings = append(plan.Warnings, unavailableAdapterWarnings(view.Config, detections)...)
		}
	}
	plan.CanApply = err == nil && !hasPendingConfig && pathErr == nil && pathValid && fingerprintErr == nil && plan.Detections[cleanupAdapterGeneric].Present
	plan.ManualRequired = !plan.CanApply
	record := &quickStartPlanRecord{
		plan: plan, networkFingerprint: fingerprint, configSummary: configSummary,
		detectedAdapters: presentAdapterNames(plan.Detections),
	}
	a.quickStartMutex.Lock()
	a.quickStartPlan = record
	a.quickStartMutex.Unlock()
	a.runtime.Logger.Info("快速流程只读计划已生成", "component", "quickstart", "interface", path.Interface, "gateway", preferredGateway(path), "result", quickStartPlanResult(plan))
	return plan, nil
}

// runQuickStart 复核并消费确认计划，在同一配置写锁内完成执行和可选持久化。
func (a *API) runQuickStart(ctx context.Context, raw json.RawMessage, emit func(any) error) (QuickStartResult, error) {
	var parameters struct {
		PlanID            string `json:"plan_id"`
		Mode              string `json:"mode"`
		ForceRangeRefresh bool   `json:"force_range_refresh"`
	}
	if err := decodeStrict(raw, &parameters); err != nil {
		return QuickStartResult{}, invalidParams(err)
	}
	if parameters.PlanID == "" {
		return QuickStartResult{}, invalidParams(errors.New("plan_id is required"))
	}
	if parameters.Mode != quickStartApplyOnce && parameters.Mode != quickStartApplyAndRemember {
		return QuickStartResult{}, invalidParams(errors.New("mode must be apply_once or apply_and_remember"))
	}
	if !a.configurationMutex.TryLock() {
		return QuickStartResult{}, &ipc.Error{Code: "conflict", Message: "configuration is being updated"}
	}
	defer a.configurationMutex.Unlock()
	record, err := a.consumeQuickStartPlan(parameters.PlanID)
	if err != nil {
		return QuickStartResult{}, err
	}
	if !record.plan.CanApply {
		return QuickStartResult{}, &ipc.Error{Code: "precondition_failed", Message: "quick-start plan requires manual configuration"}
	}
	view := a.runtime.View()
	configSummary, hasPendingConfig, err := a.currentConfigSummary()
	if err != nil || hasPendingConfig || configSummary != record.configSummary {
		return QuickStartResult{}, &ipc.Error{Code: "plan_stale", Message: "configuration changed; create and confirm a new plan"}
	}
	fingerprint, err := a.networkFingerprint(ctx, view.Config.Network.CommandTimeout.Duration())
	if err != nil || fingerprint != record.networkFingerprint {
		return QuickStartResult{}, &ipc.Error{Code: "plan_stale", Message: "network changed; create and confirm a new plan"}
	}
	path, err := a.discoverPhysicalPath(
		ctx, view.Config.Network.Interface, view.Config.Network.GatewayIPv4,
		view.Config.Network.GatewayIPv6, view.Config.Network.CommandTimeout.Duration(),
	)
	if err != nil || physicalPathSummary(path) != physicalPathSummary(record.plan.PhysicalPath) {
		return QuickStartResult{}, &ipc.Error{Code: "plan_stale", Message: "physical path changed; create and confirm a new plan"}
	}
	detections, err := a.detectManagedAdapters(ctx, path)
	if err != nil || strings.Join(presentAdapterNames(detections), "\n") != strings.Join(record.detectedAdapters, "\n") {
		return QuickStartResult{}, &ipc.Error{Code: "plan_stale", Message: "available policy adapters changed; create and confirm a new plan"}
	}
	session, err := a.buildManagedSession(path, detections)
	if err != nil {
		return QuickStartResult{}, fmt.Errorf("build confirmed quick-start session: %w", err)
	}
	transactionOffset := len(a.runtime.Routes.Transactions())
	report, runErr := a.runWithRunner(ctx, session.Runner, optimizer.RunOptions{
		ForceRangeRefresh: parameters.ForceRangeRefresh, ApplyPolicy: true,
	}, func(event optimizer.Event) error {
		if emit == nil {
			return nil
		}
		return emit(event)
	})
	result := QuickStartResult{Report: report, Mode: parameters.Mode}
	if runErr != nil {
		var protocolError *ipc.Error
		if errors.As(runErr, &protocolError) && (protocolError.Code == "conflict" || protocolError.Code == "cancelled") {
			return result, runErr
		}
		result.Status = classifyQuickStartFailure(a.runtime.Routes.Transactions()[transactionOffset:], runErr)
		result.Error = runErr.Error()
		a.runtime.Logger.Warn("快速流程执行失败", "component", "quickstart", "run_id", report.ID, "interface", path.Interface, "result", result.Status, "error", runErr)
		return result, nil
	}
	if !report.PolicyApplied {
		result.Status = "partial"
		result.Error = "策略未返回完整验证结果"
		return result, nil
	}
	result.Status = "verified"
	if parameters.Mode == quickStartApplyAndRemember {
		if a.runtime.ConfigPath == "" {
			result.Status = "partial"
			result.PersistenceWarning = "后台服务没有可写配置路径，本次策略已验证但未启用自动维护"
		} else if saveErr := a.saveConfig(a.runtime.ConfigPath, session.Config); saveErr != nil {
			result.Status = "partial"
			result.PersistenceWarning = "本次策略已验证，但自动维护配置保存失败：" + saveErr.Error()
		} else {
			a.runtime.ActivateSession(session)
			result.AutoMaintenanceEnabled = true
		}
	}
	a.runtime.Logger.Info("快速流程执行结束", "component", "quickstart", "run_id", report.ID, "interface", path.Interface, "result", result.Status, "auto_maintenance", result.AutoMaintenanceEnabled)
	return result, nil
}

// consumeQuickStartPlan 原子取出单次计划，并拒绝重放或过期确认。
func (a *API) consumeQuickStartPlan(planID string) (*quickStartPlanRecord, error) {
	a.quickStartMutex.Lock()
	defer a.quickStartMutex.Unlock()
	record := a.quickStartPlan
	if record == nil || record.plan.PlanID != planID {
		return nil, &ipc.Error{Code: "plan_not_found", Message: "quick-start plan is missing or has already been used"}
	}
	a.quickStartPlan = nil
	if !a.now().Before(record.plan.ExpiresAt) {
		return nil, &ipc.Error{Code: "plan_expired", Message: "quick-start plan expired; create a new plan"}
	}
	return record, nil
}

// currentConfigSummary 比较内存配置与磁盘配置，防止覆盖尚未重启加载的更改。
func (a *API) currentConfigSummary() (string, bool, error) {
	view := a.runtime.View()
	activeSummary, err := quickStartConfigSummary(view.Config)
	if err != nil || a.runtime.ConfigPath == "" {
		return activeSummary, false, err
	}
	if _, statErr := os.Stat(a.runtime.ConfigPath); errors.Is(statErr, os.ErrNotExist) {
		return activeSummary, false, nil
	} else if statErr != nil {
		return "", false, statErr
	}
	diskConfig, err := config.Load(a.runtime.ConfigPath, view.Config.DataDir)
	if err != nil {
		return "", false, err
	}
	diskSummary, err := quickStartConfigSummary(diskConfig)
	if err != nil {
		return "", false, err
	}
	return activeSummary, diskSummary != activeSummary, nil
}

func quickStartConfigSummary(cfg config.Config) (string, error) {
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func physicalPathSummary(path cfnetwork.PhysicalPath) string {
	encoded, _ := json.Marshal(path)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func presentAdapterNames(detections map[string]proxy.Detection) []string {
	names := make([]string, 0, len(detections))
	for name, detection := range detections {
		if detection.Present {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// quickStartEffects 将已检测适配器转换为前端可稳定映射的影响类型。
func quickStartEffects(cfg config.Config, detections map[string]proxy.Detection) []string {
	effectByAdapter := map[string]string{
		cleanupAdapterGeneric: "system_routes", cleanupAdapterMihomo: "mihomo_policy",
		cleanupAdapterSingBox: "sing_box_policy", cleanupAdapterXray: "xray_policy",
		cleanupAdapterExternal: "external_policy", cleanupAdapterHosts: "windows_hosts",
	}
	configured := map[string]bool{
		cleanupAdapterGeneric: true, cleanupAdapterMihomo: cfg.Proxy.Mihomo.Enabled,
		cleanupAdapterSingBox: cfg.Proxy.SingBox.Enabled, cleanupAdapterXray: cfg.Proxy.Xray.Enabled,
		cleanupAdapterExternal: cfg.Proxy.External.Enabled, cleanupAdapterHosts: cfg.Hosts.Enabled,
	}
	var effects []string
	for _, name := range presentAdapterNames(detections) {
		if effect := effectByAdapter[name]; effect != "" && configured[name] {
			effects = append(effects, effect)
		}
	}
	return effects
}

// unavailableAdapterWarnings 明确说明本次不会修改已配置但当前不可用的适配器。
func unavailableAdapterWarnings(cfg config.Config, detections map[string]proxy.Detection) []string {
	configured := map[string]bool{
		cleanupAdapterMihomo: cfg.Proxy.Mihomo.Enabled, cleanupAdapterSingBox: cfg.Proxy.SingBox.Enabled,
		cleanupAdapterXray: cfg.Proxy.Xray.Enabled, cleanupAdapterExternal: cfg.Proxy.External.Enabled,
		cleanupAdapterHosts: cfg.Hosts.Enabled,
	}
	var warnings []string
	for name, enabled := range configured {
		if enabled && !detections[name].Present {
			warnings = append(warnings, fmt.Sprintf("已配置的 %s 适配器当前不可用，本次不会修改它", name))
		}
	}
	sort.Strings(warnings)
	return warnings
}

// classifyQuickStartFailure 仅在新增事务均确认恢复后返回已回滚。
func classifyQuickStartFailure(transactions []cfnetwork.Transaction, runErr error) string {
	if len(transactions) == 0 || strings.Contains(strings.ToLower(runErr.Error()), "rollback") {
		return "partial"
	}
	for _, transaction := range transactions {
		if transaction.State != "rolled_back" && transaction.State != "recovered" {
			return "partial"
		}
	}
	return "rolled_back"
}

func preferredGateway(path cfnetwork.PhysicalPath) string {
	if path.GatewayIPv4 != "" {
		return path.GatewayIPv4
	}
	return path.GatewayIPv6
}

func quickStartPlanResult(plan QuickStartPlan) string {
	if plan.CanApply {
		return "ready"
	}
	return "manual_required"
}

func newQuickStartPlanID() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err == nil {
		return hex.EncodeToString(random)
	}
	return fmt.Sprintf("plan-%d", time.Now().UnixNano())
}
