package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Capabilities 声明适配器能够安全管理的策略类型和生命周期能力。
type Capabilities struct {
	Processes bool `json:"processes"`
	IPv4      bool `json:"ipv4"`
	IPv6      bool `json:"ipv6"`
	Domains   bool `json:"domains"`
	HotReload bool `json:"hot_reload"`
	Rollback  bool `json:"rollback"`
}

// Detection 表示代理内核是否存在以及可展示的非敏感版本信息。
type Detection struct {
	Present  bool   `json:"present"`
	Version  string `json:"version,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Message  string `json:"message,omitempty"`
}

// Plan 是可审计、可序列化但尚未应用的适配器变更。
type Plan struct {
	ID      string          `json:"id"`
	Adapter string          `json:"adapter"`
	Policy  DirectPolicy    `json:"policy"`
	Summary []string        `json:"summary"`
	Payload json.RawMessage `json:"payload"`
}

// Receipt 保存回滚和验证一次已应用计划所需的数据。
type Receipt struct {
	ID        string          `json:"id"`
	Adapter   string          `json:"adapter"`
	Changed   bool            `json:"changed"`
	AppliedAt time.Time       `json:"applied_at"`
	Payload   json.RawMessage `json:"payload"`
}

// Adapter 定义代理适配器必须实现的完整安全生命周期。
type Adapter interface {
	Name() string
	Capabilities() Capabilities
	Detect(context.Context) (Detection, error)
	Plan(context.Context, DirectPolicy) (Plan, error)
	Apply(context.Context, Plan) (Receipt, error)
	Verify(context.Context, DirectPolicy, Receipt) error
	Rollback(context.Context, Receipt) error
}

// ApplyResult 汇总一组适配器成功应用并验证的收据。
type ApplyResult struct {
	Receipts []Receipt `json:"receipts"`
	Skipped  []string  `json:"skipped"`
}

// Coordinator 按顺序应用适配器，并在任一步失败时逆序回滚。
type Coordinator struct {
	adapters []Adapter
	logger   *slog.Logger
}

// NewCoordinator 创建统一策略协调器；适配器顺序同时决定回滚逆序。
func NewCoordinator(adapters []Adapter, logger *slog.Logger) (*Coordinator, error) {
	if logger == nil {
		return nil, errors.New("proxy logger is required")
	}
	seen := map[string]struct{}{}
	for _, adapter := range adapters {
		if adapter == nil || adapter.Name() == "" {
			return nil, errors.New("proxy adapter and name are required")
		}
		if _, exists := seen[adapter.Name()]; exists {
			return nil, fmt.Errorf("duplicate proxy adapter %q", adapter.Name())
		}
		seen[adapter.Name()] = struct{}{}
	}
	return &Coordinator{adapters: adapters, logger: logger.With("component", "proxy")}, nil
}

// Detect 查询所有已注册适配器，不因单个内核不可用而丢弃其他结果。
func (c *Coordinator) Detect(ctx context.Context) map[string]Detection {
	result := make(map[string]Detection, len(c.adapters))
	for _, adapter := range c.adapters {
		detection, err := adapter.Detect(ctx)
		if err != nil {
			detection.Message = err.Error()
		}
		result[adapter.Name()] = detection
	}
	return result
}

// Capabilities 合并全部已配置适配器的静态能力，用于生成最小必要策略。
func (c *Coordinator) Capabilities() Capabilities {
	combined := Capabilities{}
	for _, adapter := range c.adapters {
		capability := adapter.Capabilities()
		combined.Processes = combined.Processes || capability.Processes
		combined.IPv4 = combined.IPv4 || capability.IPv4
		combined.IPv6 = combined.IPv6 || capability.IPv6
		combined.Domains = combined.Domains || capability.Domains
		combined.HotReload = combined.HotReload || capability.HotReload
		combined.Rollback = combined.Rollback || capability.Rollback
	}
	return combined
}

// Apply 将规范化策略应用到可用适配器，验证失败时回滚全部已应用项。
func (c *Coordinator) Apply(ctx context.Context, policy DirectPolicy) (ApplyResult, error) {
	normalized, err := policy.Normalize()
	if err != nil {
		return ApplyResult{}, err
	}
	result := ApplyResult{}
	activeAdapters := make([]Adapter, 0, len(c.adapters))
	for _, adapter := range c.adapters {
		detection, detectErr := adapter.Detect(ctx)
		if detectErr != nil || !detection.Present {
			result.Skipped = append(result.Skipped, adapter.Name())
			continue
		}
		if adapterSupportsAny(normalized, adapter.Capabilities()) {
			activeAdapters = append(activeAdapters, adapter)
		} else {
			result.Skipped = append(result.Skipped, adapter.Name())
		}
	}
	if len(activeAdapters) == 0 {
		return result, errors.New("no enabled proxy adapter was detected")
	}
	if err := ensurePolicyCoverage(normalized, activeAdapters); err != nil {
		return result, err
	}
	for _, adapter := range activeAdapters {
		plan, planErr := adapter.Plan(ctx, normalized)
		if planErr != nil {
			return result, c.rollbackAll(ctx, result.Receipts, fmt.Errorf("plan %s: %w", adapter.Name(), planErr))
		}
		c.logPhase(adapter.Name(), plan.ID, "plan", "completed", nil)
		receipt, applyErr := adapter.Apply(ctx, plan)
		if applyErr != nil {
			return result, c.rollbackAll(ctx, result.Receipts, fmt.Errorf("apply %s: %w", adapter.Name(), applyErr))
		}
		result.Receipts = append(result.Receipts, receipt)
		c.logPhase(adapter.Name(), receipt.ID, "apply", "completed", nil)
		if verifyErr := adapter.Verify(ctx, normalized, receipt); verifyErr != nil {
			return result, c.rollbackAll(ctx, result.Receipts, fmt.Errorf("verify %s: %w", adapter.Name(), verifyErr))
		}
		c.logPhase(adapter.Name(), receipt.ID, "verify", "completed", nil)
	}
	return result, nil
}

// Rollback 逆序撤销一组已验证收据，供跨阶段切换失败时恢复旧策略。
func (c *Coordinator) Rollback(ctx context.Context, result ApplyResult) error {
	if len(result.Receipts) == 0 {
		return nil
	}
	return errors.Join(c.rollbackReceipts(ctx, result.Receipts)...)
}

func adapterSupportsAny(policy DirectPolicy, capabilities Capabilities) bool {
	return (len(policy.Processes) > 0 && capabilities.Processes) ||
		(len(policy.IPv4CIDRs) > 0 && capabilities.IPv4) ||
		(len(policy.IPv6CIDRs) > 0 && capabilities.IPv6) ||
		(len(policy.Domains) > 0 && capabilities.Domains)
}

func ensurePolicyCoverage(policy DirectPolicy, adapters []Adapter) error {
	combined := Capabilities{}
	for _, adapter := range adapters {
		capability := adapter.Capabilities()
		combined.Processes = combined.Processes || capability.Processes
		combined.IPv4 = combined.IPv4 || capability.IPv4
		combined.IPv6 = combined.IPv6 || capability.IPv6
		combined.Domains = combined.Domains || capability.Domains
	}
	if len(policy.Processes) > 0 && !combined.Processes {
		return errors.New("no active adapter supports process DIRECT rules")
	}
	if len(policy.IPv4CIDRs) > 0 && !combined.IPv4 {
		return errors.New("no active adapter supports IPv4 DIRECT rules")
	}
	if len(policy.IPv6CIDRs) > 0 && !combined.IPv6 {
		return errors.New("no active adapter supports IPv6 DIRECT rules")
	}
	if len(policy.Domains) > 0 && !combined.Domains {
		return errors.New("no active adapter supports domain DIRECT rules")
	}
	return nil
}

func (c *Coordinator) rollbackAll(ctx context.Context, receipts []Receipt, cause error) error {
	rollbackErrors := []error{cause}
	rollbackErrors = append(rollbackErrors, c.rollbackReceipts(ctx, receipts)...)
	return errors.Join(rollbackErrors...)
}

func (c *Coordinator) rollbackReceipts(ctx context.Context, receipts []Receipt) []error {
	var rollbackErrors []error
	for index := len(receipts) - 1; index >= 0; index-- {
		receipt := receipts[index]
		adapter := c.adapterByName(receipt.Adapter)
		if adapter == nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("adapter %s is unavailable for rollback", receipt.Adapter))
			continue
		}
		if err := adapter.Rollback(ctx, receipt); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback %s: %w", adapter.Name(), err))
			c.logPhase(adapter.Name(), receipt.ID, "rollback", "failed", err)
		} else {
			c.logPhase(adapter.Name(), receipt.ID, "rollback", "completed", nil)
		}
	}
	return rollbackErrors
}

func (c *Coordinator) adapterByName(name string) Adapter {
	for _, adapter := range c.adapters {
		if adapter.Name() == name {
			return adapter
		}
	}
	return nil
}

func (c *Coordinator) logPhase(adapter, transactionID, phase, result string, operationErr error) {
	attributes := []any{"adapter", adapter, "transaction_id", transactionID, "phase", phase, "result", result}
	if operationErr != nil {
		attributes = append(attributes, "error", operationErr)
		c.logger.Error("代理策略阶段失败", attributes...)
		return
	}
	c.logger.Info("代理策略阶段完成", attributes...)
}
