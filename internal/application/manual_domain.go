package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/ipc"
	"github.com/cf-optimizer/cf-optimizer/internal/optimizer"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

const (
	manualDomainActionAdd      = "add"
	manualDomainActionRemove   = "remove"
	manualDomainStatePending   = "pending"
	manualDomainStateRemoving  = "removing"
	manualDomainStateVerifying = "verifying"
	manualDomainStateVerified  = "verified"
	manualDomainStateDeferred  = "deferred"
	manualDomainStateFailed    = "failed"
	manualDomainStateRemoved   = "removed"
	// manualDomainOperationTimeout 限制单次后台域名策略维护的最长时间。
	manualDomainOperationTimeout = 10 * time.Minute
	// manualDomainOperationPollInterval 控制后台域名操作等待现有任务结束的轮询频率。
	manualDomainOperationPollInterval = 500 * time.Millisecond
)

// ManualDomainOperation 描述手动域名新增或删除的异步状态。
type ManualDomainOperation struct {
	OperationID string    `json:"operation_id"`
	Domain      string    `json:"domain"`
	Action      string    `json:"action"`
	State       string    `json:"state"`
	Message     string    `json:"message,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// accelerationDomainsSnapshot 合并域名发现快照和当前手动域名异步操作。
func (a *API) accelerationDomainsSnapshot() DomainDiscoveryResult {
	result := a.runtime.domainDiscoverySnapshot()
	result.Operations = a.manualDomainOperations()
	return result
}

// manualDomainOperations 返回隔离后的异步操作状态，供界面轮询展示。
func (a *API) manualDomainOperations() []ManualDomainOperation {
	a.manualDomainMutex.RLock()
	result := make([]ManualDomainOperation, 0, len(a.manualDomainOps))
	for _, operation := range a.manualDomainOps {
		result = append(result, operation)
	}
	a.manualDomainMutex.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].Domain < result[j].Domain
		}
		return result[i].UpdatedAt.Before(result[j].UpdatedAt)
	})
	return result
}

// addManualDomain 保存新域名并异步触发当前策略验证。
func (a *API) addManualDomain(raw json.RawMessage) (ManualDomainOperation, error) {
	var parameters struct {
		Domain string `json:"domain"`
	}
	if err := decodeStrict(raw, &parameters); err != nil {
		return ManualDomainOperation{}, invalidParams(err)
	}
	if !a.configurationMutex.TryLock() {
		return ManualDomainOperation{}, &ipc.Error{Code: "conflict", Message: "configuration update or manual domain operation is already active"}
	}
	defer a.configurationMutex.Unlock()
	if a.manualDomainOperationActive() {
		return ManualDomainOperation{}, &ipc.Error{Code: "conflict", Message: "another manual domain operation is already active"}
	}
	view := a.runtime.View()
	domain, err := normalizeAndValidateManualDomain(view.Config, parameters.Domain)
	if err != nil {
		return ManualDomainOperation{}, invalidParams(err)
	}
	if a.runtime.ConfigPath == "" {
		return ManualDomainOperation{}, &ipc.Error{Code: "precondition_failed", Message: "daemon has no configured config file path"}
	}
	if containsNormalizedDomain(view.Config.Acceleration.ExcludedDomains, domain) {
		return ManualDomainOperation{}, invalidParams(fmt.Errorf("domain %q is excluded from acceleration", domain))
	}
	manualDomains := normalizedManualDomains(view.Config.Acceleration.ManualDomains)
	if containsNormalizedDomain(manualDomains, domain) {
		return ManualDomainOperation{}, invalidParams(fmt.Errorf("domain %q is already configured", domain))
	}
	nextConfig := view.Config
	nextConfig.Acceleration.ManualDomains = append(manualDomains, domain)
	nextConfig.Acceleration.ManualMappings = cloneManualMappingConfig(view.Config.Acceleration.ManualMappings)
	pruneOrphanedManualMappings(&nextConfig)
	nextConfig.ApplyDefaults()
	if err := nextConfig.Validate(); err != nil {
		return ManualDomainOperation{}, invalidParams(fmt.Errorf("validate manual domain configuration: %w", err))
	}
	operation := a.reserveManualDomainOperation(domain, manualDomainActionAdd, manualDomainStatePending, "已写入配置，等待后台验证")
	if err := a.saveConfig(a.runtime.ConfigPath, nextConfig); err != nil {
		a.finishManualDomainOperation(operation.OperationID, manualDomainStateFailed, "配置保存失败: "+err.Error())
		return ManualDomainOperation{}, err
	}
	// 先发布配置快照，让界面立即看到新域名；Runner 会在后台任务中原子刷新策略。
	a.runtime.setConfigSnapshot(nextConfig)
	a.startManualDomainWorker(operation, func(ctx context.Context) error {
		return a.verifyAddedManualDomain(ctx, operation, view.Config, domain)
	})
	return operation, nil
}

// removeManualDomain 延迟清理配置，先验证现有策略已移除指定域名。
func (a *API) removeManualDomain(raw json.RawMessage) (ManualDomainOperation, error) {
	var parameters struct {
		Domain string `json:"domain"`
	}
	if err := decodeStrict(raw, &parameters); err != nil {
		return ManualDomainOperation{}, invalidParams(err)
	}
	if !a.configurationMutex.TryLock() {
		return ManualDomainOperation{}, &ipc.Error{Code: "conflict", Message: "configuration update or manual domain operation is already active"}
	}
	defer a.configurationMutex.Unlock()
	if a.manualDomainOperationActive() {
		return ManualDomainOperation{}, &ipc.Error{Code: "conflict", Message: "another manual domain operation is already active"}
	}
	view := a.runtime.View()
	domain, err := normalizeAndValidateManualDomain(view.Config, parameters.Domain)
	if err != nil {
		return ManualDomainOperation{}, invalidParams(err)
	}
	manualDomains := normalizedManualDomains(view.Config.Acceleration.ManualDomains)
	if !containsNormalizedDomain(manualDomains, domain) {
		return ManualDomainOperation{}, invalidParams(fmt.Errorf("domain %q is not configured in acceleration.manual_domains", domain))
	}
	if a.runtime.ConfigPath == "" {
		return ManualDomainOperation{}, &ipc.Error{Code: "precondition_failed", Message: "daemon has no configured config file path"}
	}
	operation := a.reserveManualDomainOperation(domain, manualDomainActionRemove, manualDomainStateRemoving, "已标记移除，等待后台验证策略清理")
	previousConfig := view.Config
	a.startManualDomainWorker(operation, func(ctx context.Context) error {
		return a.verifyRemovedManualDomain(ctx, operation, previousConfig, domain)
	})
	return operation, nil
}

// normalizeAndValidateManualDomain 复用完整配置校验，确保 IPC 新增域名遵循同一安全边界。
func normalizeAndValidateManualDomain(current config.Config, rawDomain string) (string, error) {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rawDomain)), ".")
	if domain == "" {
		return "", errors.New("domain is required")
	}
	candidate := current
	if !containsNormalizedDomain(current.Acceleration.ManualDomains, domain) {
		candidate.Acceleration.ManualDomains = append(normalizedManualDomains(current.Acceleration.ManualDomains), domain)
	}
	candidate.ApplyDefaults()
	if err := candidate.Validate(); err != nil {
		return "", err
	}
	return domain, nil
}

// normalizedManualDomains 按配置优先级顺序返回规范化且去重的手动域名。
func normalizedManualDomains(domains []string) []string {
	result := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for _, rawDomain := range domains {
		domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rawDomain)), ".")
		if domain == "" {
			continue
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	return result
}

// containsNormalizedDomain 使用配置规范化规则判断域名是否存在。
func containsNormalizedDomain(domains []string, expected string) bool {
	for _, domain := range domains {
		if strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".") == expected {
			return true
		}
	}
	return false
}

// reserveManualDomainOperation 创建并发布一个新的手动域名后台操作。
func (a *API) reserveManualDomainOperation(domain, action, state, message string) ManualDomainOperation {
	now := time.Now().UTC()
	if a.now != nil {
		now = a.now().UTC()
	}
	operation := ManualDomainOperation{
		OperationID: fmt.Sprintf("manual-domain-%d", now.UnixNano()), Domain: domain, Action: action,
		State: state, Message: message, UpdatedAt: now,
	}
	a.manualDomainMutex.Lock()
	if a.manualDomainOps == nil {
		a.manualDomainOps = map[string]ManualDomainOperation{}
	}
	a.manualDomainOps[domain] = operation
	a.manualDomainMutex.Unlock()
	return operation
}

// finishManualDomainOperation 更新指定操作的状态和用户可读消息。
func (a *API) finishManualDomainOperation(operationID, state, message string) {
	a.manualDomainMutex.Lock()
	defer a.manualDomainMutex.Unlock()
	for domain, operation := range a.manualDomainOps {
		if operation.OperationID != operationID {
			continue
		}
		operation.State = state
		operation.Message = message
		operation.UpdatedAt = time.Now().UTC()
		a.manualDomainOps[domain] = operation
		return
	}
}

// manualDomainOperationActive 判断是否已有会修改当前策略的域名操作。
func (a *API) manualDomainOperationActive() bool {
	a.manualDomainMutex.RLock()
	defer a.manualDomainMutex.RUnlock()
	for _, operation := range a.manualDomainOps {
		if isManualDomainOperationActive(operation.State) {
			return true
		}
	}
	return false
}

// isManualDomainOperationActive 区分进行中与终态操作。
func isManualDomainOperationActive(state string) bool {
	switch state {
	case manualDomainStatePending, manualDomainStateRemoving, manualDomainStateVerifying:
		return true
	default:
		return false
	}
}

// startManualDomainWorker 在 IPC 返回后继续执行耗时的物理策略验证。
func (a *API) startManualDomainWorker(operation ManualDomainOperation, task func(context.Context) error) {
	go func() {
		var ctx context.Context
		var cancel context.CancelFunc
		for {
			candidateContext, candidateCancel := context.WithTimeout(context.Background(), manualDomainOperationTimeout)
			if a.setActiveCancel(candidateCancel) {
				ctx = candidateContext
				cancel = candidateCancel
				break
			}
			candidateCancel()
			a.finishManualDomainOperation(operation.OperationID, operation.State, "正在等待当前优选或策略维护任务结束")
			time.Sleep(manualDomainOperationPollInterval)
		}
		defer cancel()
		defer a.clearActiveCancel()
		a.finishManualDomainOperation(operation.OperationID, manualDomainStateVerifying, "正在生成并验证当前直连策略")
		a.setActiveEvent(optimizerEventForManualDomain(operation, "stage.started", "正在生成并验证当前直连策略"))
		if err := task(ctx); err != nil {
			a.finishManualDomainOperation(operation.OperationID, manualDomainStateFailed, err.Error())
			a.setActiveEvent(optimizerEventForManualDomain(operation, "stage.failed", err.Error()))
			return
		}
	}()
}

// optimizerEventForManualDomain 构造可由现有状态轮询展示的域名维护事件。
func optimizerEventForManualDomain(operation ManualDomainOperation, eventType, message string) optimizer.Event {
	return optimizer.Event{RunID: operation.OperationID, Type: eventType, Stage: "domain", Message: message, Timestamp: time.Now().UTC()}
}

// verifyAddedManualDomain 只刷新新增后的域名集合，并在证据完整后持久化映射。
func (a *API) verifyAddedManualDomain(ctx context.Context, operation ManualDomainOperation, previous config.Config, domain string) error {
	view := a.runtime.View()
	if view.Runner == nil {
		return errors.New("optimizer runner is unavailable for manual domain verification")
	}
	policyBefore := a.runtime.Store.Snapshot().Policy
	policyRefreshed, err := view.Runner.UpdateAccelerationDomains(ctx, normalizedManualDomains(view.Config.Acceleration.ManualDomains), slices.Clone(view.Config.Acceleration.ExcludedDomains))
	if err != nil {
		return fmt.Errorf("verify added manual domain %s: %w", domain, err)
	}
	stateAfter := a.runtime.Store.Snapshot()
	if !policyRefreshed {
		if policyBefore == nil {
			a.finishManualDomainOperation(operation.OperationID, manualDomainStateDeferred, "域名已保存，当前没有已验证策略，等待下一次优选后验证")
			return nil
		}
		_, _ = view.Runner.UpdateAccelerationDomains(context.WithoutCancel(ctx), normalizedManualDomains(previous.Acceleration.ManualDomains), slices.Clone(previous.Acceleration.ExcludedDomains))
		return errors.New("当前策略未完成刷新，域名已保存但尚未验证")
	}
	address, ok := verifiedManualDomainAddress(view.Config, stateAfter, domain)
	if !ok {
		_, _ = view.Runner.UpdateAccelerationDomains(context.WithoutCancel(ctx), normalizedManualDomains(previous.Acceleration.ManualDomains), slices.Clone(previous.Acceleration.ExcludedDomains))
		return fmt.Errorf("manual domain %s policy verification evidence is incomplete", domain)
	}
	nextConfig := view.Config
	nextConfig.Acceleration.ManualMappings = cloneManualMappingConfig(view.Config.Acceleration.ManualMappings)
	nextConfig.Acceleration.ManualMappings[domain] = address
	nextConfig.ApplyDefaults()
	if err := nextConfig.Validate(); err != nil {
		_, _ = view.Runner.UpdateAccelerationDomains(context.WithoutCancel(ctx), normalizedManualDomains(previous.Acceleration.ManualDomains), slices.Clone(previous.Acceleration.ExcludedDomains))
		return fmt.Errorf("validate verified manual mapping %s: %w", domain, err)
	}
	a.configurationMutex.Lock()
	persistErr := a.saveConfig(a.runtime.ConfigPath, nextConfig)
	a.configurationMutex.Unlock()
	if persistErr != nil {
		_, _ = view.Runner.UpdateAccelerationDomains(context.WithoutCancel(ctx), normalizedManualDomains(previous.Acceleration.ManualDomains), slices.Clone(previous.Acceleration.ExcludedDomains))
		return fmt.Errorf("persist verified manual mapping %s: %w", domain, persistErr)
	}
	a.runtime.setConfigSnapshot(nextConfig)
	a.finishManualDomainOperation(operation.OperationID, manualDomainStateVerified, fmt.Sprintf("已验证并应用 %s", address))
	return nil
}

// verifyRemovedManualDomain 先验证新策略已排除域名，再清理配置和映射。
func (a *API) verifyRemovedManualDomain(ctx context.Context, operation ManualDomainOperation, previous config.Config, domain string) error {
	view := a.runtime.View()
	if view.Runner == nil {
		return errors.New("optimizer runner is unavailable for manual domain removal")
	}
	previousPolicy := a.runtime.Store.Snapshot().Policy
	manualDomains := normalizedManualDomains(previous.Acceleration.ManualDomains)
	nextManualDomains := make([]string, 0, len(manualDomains)-1)
	for _, configuredDomain := range manualDomains {
		if configuredDomain != domain {
			nextManualDomains = append(nextManualDomains, configuredDomain)
		}
	}
	policyRefreshed, err := view.Runner.UpdateAccelerationDomains(ctx, nextManualDomains, slices.Clone(previous.Acceleration.ExcludedDomains))
	if err != nil {
		return fmt.Errorf("verify removal of manual domain %s: %w", domain, err)
	}
	stateAfter := a.runtime.Store.Snapshot()
	if previousPolicy != nil && (!policyRefreshed || policyContainsManualDomain(stateAfter.Policy, domain)) {
		_, _ = view.Runner.UpdateAccelerationDomains(context.WithoutCancel(ctx), manualDomains, slices.Clone(previous.Acceleration.ExcludedDomains))
		return fmt.Errorf("manual domain %s is still present in the verified policy", domain)
	}
	nextConfig := previous
	nextConfig.Acceleration.ManualDomains = nextManualDomains
	nextConfig.Acceleration.ManualMappings = cloneManualMappingConfig(previous.Acceleration.ManualMappings)
	delete(nextConfig.Acceleration.ManualMappings, domain)
	pruneOrphanedManualMappings(&nextConfig)
	nextConfig.ApplyDefaults()
	if err := nextConfig.Validate(); err != nil {
		_, _ = view.Runner.UpdateAccelerationDomains(context.WithoutCancel(ctx), manualDomains, slices.Clone(previous.Acceleration.ExcludedDomains))
		return fmt.Errorf("validate final manual domain removal: %w", err)
	}
	a.configurationMutex.Lock()
	persistErr := a.saveConfig(a.runtime.ConfigPath, nextConfig)
	a.configurationMutex.Unlock()
	if persistErr != nil {
		_, _ = view.Runner.UpdateAccelerationDomains(context.WithoutCancel(ctx), manualDomains, slices.Clone(previous.Acceleration.ExcludedDomains))
		return fmt.Errorf("persist final manual domain removal: %w", persistErr)
	}
	a.runtime.setConfigSnapshot(nextConfig)
	a.finishManualDomainOperation(operation.OperationID, manualDomainStateRemoved, "已验证策略移除并完成配置清理")
	return nil
}

// verifiedManualDomainAddress 返回具备完整验证证据的单地址手动映射。
func verifiedManualDomainAddress(cfg config.Config, state store.State, domain string) (string, bool) {
	if state.Policy == nil {
		return "", false
	}
	address, ok := policyDomainAddress(state.Policy, domain)
	if !ok {
		return "", false
	}
	record, exists := state.DiscoveredDomains[domain]
	if !exists || !record.CloudflareVerified || !record.PreflightVerified {
		return "", false
	}
	if cfg.Acceleration.ManualDownloadTest && !record.DownloadVerified {
		return "", false
	}
	return address, true
}

// policyContainsManualDomain 判断已验证策略是否仍包含指定精确域名映射。
func policyContainsManualDomain(policy *store.PolicySnapshot, domain string) bool {
	_, ok := policyDomainAddress(policy, domain)
	return ok
}

// policyDomainAddress 从策略快照读取规范化域名的唯一映射地址。
func policyDomainAddress(policy *store.PolicySnapshot, domain string) (string, bool) {
	if policy == nil {
		return "", false
	}
	for _, mapping := range policy.DomainMappings {
		if strings.TrimSuffix(strings.ToLower(strings.TrimSpace(mapping.Domain)), ".") != domain || len(mapping.Addresses) != 1 {
			continue
		}
		return strings.TrimSpace(mapping.Addresses[0]), true
	}
	return "", false
}
