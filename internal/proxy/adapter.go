package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"
)

// Capabilities 声明适配器能够安全管理的策略类型和生命周期能力。
type Capabilities struct {
	Processes      bool `json:"processes"`
	IPv4           bool `json:"ipv4"`
	IPv6           bool `json:"ipv6"`
	Domains        bool `json:"domains"`
	DomainMappings bool `json:"domain_mappings"`
	HotReload      bool `json:"hot_reload"`
	Rollback       bool `json:"rollback"`
}

var (
	// ErrNoActiveAdapters 表示策略应用前没有检测到任何可参与本次策略的活动适配器。
	ErrNoActiveAdapters = errors.New("no enabled proxy adapter was detected")
	// ErrDomainMappingsUnavailable 表示策略应用前的活动适配器无法维护域名映射。
	ErrDomainMappingsUnavailable = errors.New("no active adapter supports domain mappings")
)

// Detection 表示代理内核是否存在以及可展示的非敏感版本信息。
type Detection struct {
	Present    bool   `json:"present"`
	Manageable bool   `json:"manageable"`
	Version    string `json:"version,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	Message    string `json:"message,omitempty"`
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

// AdapterResolver 在已缓存适配器失效时重新发现同名代理适配器。
// resolver 只能返回相同 Name 的适配器，协调器会再次执行 Detect 验证。
type AdapterResolver func(context.Context) (Adapter, error)

// ReceiptJournal 在策略提交前持久化已应用收据，避免进程退出后失去回滚依据。
type ReceiptJournal interface {
	Begin(DirectPolicy) error
	Record(Receipt) error
	Remove([]Receipt) error
}

// ConflictCleaner 仅在常规哈希保护拒绝回滚时清理可证明由适配器管理的残留。
type ConflictCleaner interface {
	CleanupConflict(context.Context, []Receipt) error
}

// ApplyResult 汇总一组适配器成功应用并验证的收据。
type ApplyResult struct {
	Receipts []Receipt `json:"receipts"`
	Skipped  []string  `json:"skipped"`
}

// BenchmarkPathEvidence 描述一次真实测速 Socket 在代理控制面中的 DIRECT 证据。
type BenchmarkPathEvidence struct {
	Adapter           string `json:"adapter"`
	Interface         string `json:"interface,omitempty"`
	Target            string `json:"target"`
	GuardApplied      bool   `json:"guard_applied"`
	SocketBound       bool   `json:"socket_bound"`
	ProxyObserved     bool   `json:"proxy_observed"`
	DirectVerified    bool   `json:"direct_verified"`
	PhysicalRouteUsed bool   `json:"physical_route_used"`
	Rule              string `json:"rule,omitempty"`
	RulePayload       string `json:"rule_payload,omitempty"`
	Verification      string `json:"verification"`
}

// BenchmarkGuardResult 保存临时测速保护的回滚收据和对外证据。
type BenchmarkGuardResult struct {
	Receipts []Receipt               `json:"-"`
	Evidence []BenchmarkPathEvidence `json:"evidence,omitempty"`
}

// BenchmarkGuard 为测速阶段提供与最终策略隔离的可回滚 DIRECT 保护。
type BenchmarkGuard interface {
	BeginBenchmarkGuard(context.Context, DirectPolicy, []netip.Addr) (BenchmarkGuardResult, error)
	EndBenchmarkGuard(context.Context, BenchmarkGuardResult) error
}

type benchmarkPathVerifier interface {
	VerifyBenchmarkPath(context.Context, []netip.Addr) (BenchmarkPathEvidence, error)
}

// DomainVerificationKind 标识域名应用验证失败的原因类别。
type DomainVerificationKind string

const (
	// DomainVerificationCandidateUnreachable 表示候选地址无法完成 HTTPS 连接。
	DomainVerificationCandidateUnreachable DomainVerificationKind = "candidate_unreachable"
	// DomainVerificationMappingNotPropagated 表示连接成功但系统映射仍未切换到候选地址。
	DomainVerificationMappingNotPropagated DomainVerificationKind = "mapping_not_propagated"
)

// DomainVerificationError 标识真实连接验证失败的精确域名和候选地址。
type DomainVerificationError struct {
	Domain  string
	Address string
	Kind    DomainVerificationKind
	Err     error
}

// Error 返回包含失败域名的验证错误。
func (e *DomainVerificationError) Error() string {
	if e == nil {
		return "domain connection verification failed"
	}
	if e.Kind == "" {
		return fmt.Sprintf("verify domain %s connection: %v", e.Domain, e.Err)
	}
	return fmt.Sprintf("verify domain %s connection (%s): %v", e.Domain, e.Kind, e.Err)
}

// Unwrap 保留底层网络错误链。
func (e *DomainVerificationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Coordinator 按顺序应用适配器，并在任一步失败时逆序回滚。
type Coordinator struct {
	adapters        []Adapter
	logger          *slog.Logger
	journal         ReceiptJournal
	mu              sync.RWMutex
	resolvers       map[string]AdapterResolver
	receiptAdapters map[string]Adapter
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
	return &Coordinator{
		adapters:        append([]Adapter(nil), adapters...),
		logger:          logger.With("component", "proxy"),
		resolvers:       make(map[string]AdapterResolver),
		receiptAdapters: make(map[string]Adapter),
	}, nil
}

// SetReceiptJournal 注入与状态存储绑定的待提交收据日志。
func (c *Coordinator) SetReceiptJournal(journal ReceiptJournal) {
	c.journal = journal
}

// SetAdapterResolver 为指定适配器注册一次性重新发现回调；传入 nil 会移除回调。
func (c *Coordinator) SetAdapterResolver(name string, resolver AdapterResolver) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resolvers == nil {
		c.resolvers = make(map[string]AdapterResolver)
	}
	if resolver == nil {
		delete(c.resolvers, name)
		return
	}
	c.resolvers[name] = resolver
}

// Detect 查询所有已注册适配器，不因单个内核不可用而丢弃其他结果。
func (c *Coordinator) Detect(ctx context.Context) map[string]Detection {
	adapters := c.adapterSnapshot()
	result := make(map[string]Detection, len(adapters))
	for _, configured := range adapters {
		adapter, detection, err := c.resolveAdapter(ctx, configured)
		if err != nil {
			detection.Message = err.Error()
		}
		if detection.Present && err == nil {
			detection.Manageable = true
		}
		result[configured.Name()] = detection
		if adapter == nil {
			continue
		}
	}
	return result
}

// Capabilities 合并全部已配置适配器的静态能力，用于生成最小必要策略。
func (c *Coordinator) Capabilities() Capabilities {
	combined := Capabilities{}
	for _, adapter := range c.adapterSnapshot() {
		capability := adapter.Capabilities()
		combined = mergeCapabilities(combined, capability)
	}
	return combined
}

// ActiveCapabilities 只合并当前控制端检测成功的适配器能力；检测失败的适配器不会影响策略生成。
func (c *Coordinator) ActiveCapabilities(ctx context.Context) Capabilities {
	combined := Capabilities{}
	for _, configured := range c.adapterSnapshot() {
		adapter, detection, err := c.resolveAdapter(ctx, configured)
		if err != nil || !detection.Present || adapter == nil {
			if err != nil {
				c.logger.Warn("刷新代理能力失败", "adapter", configured.Name(), "result", "deferred", "error", err)
			}
			continue
		}
		combined = mergeCapabilities(combined, adapter.Capabilities())
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
	configuredAdapters := c.adapterSnapshot()
	activeAdapters := make([]Adapter, 0, len(configuredAdapters))
	for _, configured := range configuredAdapters {
		adapter, detection, detectErr := c.resolveAdapter(ctx, configured)
		if detectErr != nil || !detection.Present {
			result.Skipped = append(result.Skipped, configured.Name())
			continue
		}
		if adapterSupportsAny(normalized, adapter.Capabilities()) {
			activeAdapters = append(activeAdapters, adapter)
		} else {
			result.Skipped = append(result.Skipped, configured.Name())
		}
	}
	if len(activeAdapters) == 0 {
		return result, ErrNoActiveAdapters
	}
	if err := ensurePolicyCoverage(normalized, activeAdapters); err != nil {
		return result, err
	}
	if c.journal != nil {
		if err := c.journal.Begin(normalized); err != nil {
			return result, fmt.Errorf("begin proxy receipt journal: %w", err)
		}
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
		c.rememberReceiptAdapter(receipt, adapter)
		if c.journal != nil {
			if journalErr := c.journal.Record(receipt); journalErr != nil {
				return result, c.rollbackAll(ctx, result.Receipts, fmt.Errorf("persist %s receipt: %w", adapter.Name(), journalErr))
			}
		}
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
	rollbackErrors := c.rollbackReceipts(context.WithoutCancel(ctx), result.Receipts)
	if len(rollbackErrors) == 0 && c.journal != nil {
		if err := c.journal.Remove(result.Receipts); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("remove rolled back proxy receipts: %w", err))
		}
	}
	return errors.Join(rollbackErrors...)
}

// Cleanup 逆序回滚收据；常规回滚冲突时仅调用适配器的受管残留清理能力。
func (c *Coordinator) Cleanup(ctx context.Context, result ApplyResult) error {
	if len(result.Receipts) == 0 {
		return nil
	}
	cleanedAdapters := map[string]bool{}
	var cleanupErrors []error
	for index := len(result.Receipts) - 1; index >= 0; index-- {
		receipt := result.Receipts[index]
		if cleanedAdapters[receipt.Adapter] {
			continue
		}
		adapter := c.adapterForReceipt(receipt)
		if adapter == nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("adapter %s is unavailable for cleanup", receipt.Adapter))
			continue
		}
		if err := adapter.Rollback(ctx, receipt); err == nil {
			c.logPhase(adapter.Name(), receipt.ID, "rollback", "completed", nil)
			continue
		} else if cleaner, ok := adapter.(ConflictCleaner); ok {
			rollbackErr := err
			if cleanupErr := cleaner.CleanupConflict(ctx, receiptsForAdapter(result.Receipts, receipt.Adapter)); cleanupErr == nil {
				cleanedAdapters[receipt.Adapter] = true
				c.logPhase(adapter.Name(), receipt.ID, "cleanup", "completed", nil)
				continue
			} else {
				cleanupErrors = append(cleanupErrors, errors.Join(
					fmt.Errorf("rollback %s: %w", adapter.Name(), rollbackErr),
					fmt.Errorf("clean conflicting %s state: %w", adapter.Name(), cleanupErr),
				))
				c.logPhase(adapter.Name(), receipt.ID, "cleanup", "failed", cleanupErr)
				continue
			}
		} else {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("rollback %s: %w", adapter.Name(), err))
			c.logPhase(adapter.Name(), receipt.ID, "rollback", "failed", err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func receiptsForAdapter(receipts []Receipt, adapter string) []Receipt {
	selected := make([]Receipt, 0, len(receipts))
	for _, receipt := range receipts {
		if receipt.Adapter == adapter {
			selected = append(selected, receipt)
		}
	}
	return selected
}

// BeginBenchmarkGuard 只调用具备连接证据能力的适配器，应用并验证临时 DIRECT 规则。
func (c *Coordinator) BeginBenchmarkGuard(ctx context.Context, policy DirectPolicy, targets []netip.Addr) (BenchmarkGuardResult, error) {
	normalized, err := policy.Normalize()
	if err != nil {
		return BenchmarkGuardResult{}, err
	}
	result := BenchmarkGuardResult{}
	for _, configured := range c.adapterSnapshot() {
		adapter, detection, detectErr := c.resolveAdapter(ctx, configured)
		verifier, supported := adapter.(benchmarkPathVerifier)
		if !supported {
			continue
		}
		if detectErr != nil {
			// 代理控制面离线时保留真实物理路由回退机会；可用适配器仍会继续建立 guard。
			c.logger.Warn("benchmark guard 适配器不可用", "adapter", configured.Name(), "result", "deferred", "error", detectErr)
			continue
		}
		if !detection.Present {
			continue
		}
		if !adapterSupportsAny(normalized, adapter.Capabilities()) {
			return result, c.rollbackBenchmarkGuard(ctx, result, fmt.Errorf("benchmark guard %s does not support the temporary policy", adapter.Name()))
		}
		plan, planErr := adapter.Plan(ctx, normalized)
		if planErr != nil {
			return result, c.rollbackBenchmarkGuard(ctx, result, fmt.Errorf("plan benchmark guard %s: %w", adapter.Name(), planErr))
		}
		c.logPhase(adapter.Name(), plan.ID, "benchmark_plan", "completed", nil)
		receipt, applyErr := adapter.Apply(ctx, plan)
		if applyErr != nil {
			return result, c.rollbackBenchmarkGuard(ctx, result, fmt.Errorf("apply benchmark guard %s: %w", adapter.Name(), applyErr))
		}
		result.Receipts = append(result.Receipts, receipt)
		c.rememberReceiptAdapter(receipt, adapter)
		c.logPhase(adapter.Name(), receipt.ID, "benchmark_apply", "completed", nil)
		if verifyErr := adapter.Verify(ctx, normalized, receipt); verifyErr != nil {
			return result, c.rollbackBenchmarkGuard(ctx, result, fmt.Errorf("verify benchmark guard %s: %w", adapter.Name(), verifyErr))
		}
		evidence, evidenceErr := verifier.VerifyBenchmarkPath(ctx, targets)
		if evidenceErr != nil {
			return result, c.rollbackBenchmarkGuard(ctx, result, fmt.Errorf("verify benchmark path %s: %w", adapter.Name(), evidenceErr))
		}
		evidence.Adapter = adapter.Name()
		evidence.GuardApplied = true
		result.Evidence = append(result.Evidence, evidence)
		c.logPhase(adapter.Name(), receipt.ID, "benchmark_verify", "completed", nil)
	}
	return result, nil
}

// EndBenchmarkGuard 在最终策略应用前逆序撤销全部临时测速规则。
func (c *Coordinator) EndBenchmarkGuard(ctx context.Context, result BenchmarkGuardResult) error {
	return errors.Join(c.rollbackReceipts(ctx, result.Receipts)...)
}

// rollbackBenchmarkGuard 在临时保护任一阶段失败时保留原始错误并逆序恢复已应用项。
func (c *Coordinator) rollbackBenchmarkGuard(ctx context.Context, result BenchmarkGuardResult, cause error) error {
	rollbackErrors := []error{cause}
	rollbackErrors = append(rollbackErrors, c.rollbackReceipts(ctx, result.Receipts)...)
	return errors.Join(rollbackErrors...)
}

func adapterSupportsAny(policy DirectPolicy, capabilities Capabilities) bool {
	return (len(policy.Processes) > 0 && capabilities.Processes) ||
		(len(policy.IPv4CIDRs) > 0 && capabilities.IPv4) ||
		(len(policy.IPv6CIDRs) > 0 && capabilities.IPv6) ||
		(len(policy.Domains) > 0 && capabilities.Domains) ||
		(len(policy.DomainMappings) > 0 && capabilities.DomainMappings)
}

func ensurePolicyCoverage(policy DirectPolicy, adapters []Adapter) error {
	combined := Capabilities{}
	for _, adapter := range adapters {
		capability := adapter.Capabilities()
		combined = mergeCapabilities(combined, capability)
	}
	// 进程规则是可选附加能力；缺少它不能阻塞 IP 或域名映射策略。
	if len(policy.IPv4CIDRs) > 0 && !combined.IPv4 {
		return errors.New("no active adapter supports IPv4 DIRECT rules")
	}
	if len(policy.IPv6CIDRs) > 0 && !combined.IPv6 {
		return errors.New("no active adapter supports IPv6 DIRECT rules")
	}
	if len(policy.Domains) > 0 && !combined.Domains {
		return errors.New("no active adapter supports domain DIRECT rules")
	}
	if len(policy.DomainMappings) > 0 && !combined.DomainMappings {
		return ErrDomainMappingsUnavailable
	}
	return nil
}

func (c *Coordinator) rollbackAll(ctx context.Context, receipts []Receipt, cause error) error {
	rollbackErrors := []error{cause}
	receiptErrors := c.rollbackReceipts(context.WithoutCancel(ctx), receipts)
	rollbackErrors = append(rollbackErrors, receiptErrors...)
	if len(receiptErrors) == 0 && c.journal != nil {
		if err := c.journal.Remove(receipts); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("remove rolled back proxy receipts: %w", err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func (c *Coordinator) rollbackReceipts(ctx context.Context, receipts []Receipt) []error {
	var rollbackErrors []error
	for index := len(receipts) - 1; index >= 0; index-- {
		receipt := receipts[index]
		adapter := c.adapterForReceipt(receipt)
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
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, adapter := range c.adapters {
		if adapter.Name() == name {
			return adapter
		}
	}
	return nil
}

func (c *Coordinator) adapterSnapshot() []Adapter {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Adapter(nil), c.adapters...)
}

func (c *Coordinator) resolverFor(name string) AdapterResolver {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.resolvers[name]
}

func (c *Coordinator) replaceAdapter(name string, adapter Adapter) {
	if adapter == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for index, current := range c.adapters {
		if current.Name() == name {
			c.adapters[index] = adapter
			return
		}
	}
}

func (c *Coordinator) rememberReceiptAdapter(receipt Receipt, adapter Adapter) {
	if receipt.ID == "" || adapter == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.receiptAdapters == nil {
		c.receiptAdapters = make(map[string]Adapter)
	}
	c.receiptAdapters[receipt.ID] = adapter
}

func (c *Coordinator) adapterForReceipt(receipt Receipt) Adapter {
	c.mu.RLock()
	if adapter := c.receiptAdapters[receipt.ID]; adapter != nil {
		c.mu.RUnlock()
		return adapter
	}
	for _, adapter := range c.adapters {
		if adapter.Name() == receipt.Adapter {
			c.mu.RUnlock()
			return adapter
		}
	}
	c.mu.RUnlock()
	return nil
}

func (c *Coordinator) resolveAdapter(ctx context.Context, configured Adapter) (Adapter, Detection, error) {
	if configured == nil {
		return nil, Detection{}, errors.New("proxy adapter is nil")
	}
	detection, detectErr := configured.Detect(ctx)
	if detectErr == nil && detection.Present {
		return configured, detection, nil
	}
	resolver := c.resolverFor(configured.Name())
	if resolver == nil {
		return configured, detection, detectErr
	}
	refreshed, refreshErr := resolver(ctx)
	if refreshErr != nil {
		if detectErr != nil {
			return configured, detection, errors.Join(detectErr, fmt.Errorf("rediscover %s: %w", configured.Name(), refreshErr))
		}
		return configured, detection, fmt.Errorf("rediscover %s: %w", configured.Name(), refreshErr)
	}
	if refreshed == nil || refreshed.Name() != configured.Name() {
		return configured, detection, fmt.Errorf("rediscover %s returned an incompatible adapter", configured.Name())
	}
	refreshedDetection, refreshedErr := refreshed.Detect(ctx)
	if refreshedErr != nil || !refreshedDetection.Present {
		if refreshedErr == nil {
			refreshedErr = errors.New("rediscovered adapter is not reachable")
		}
		return configured, refreshedDetection, fmt.Errorf("detect rediscovered %s: %w", configured.Name(), refreshedErr)
	}
	c.replaceAdapter(configured.Name(), refreshed)
	return refreshed, refreshedDetection, nil
}

func mergeCapabilities(left, right Capabilities) Capabilities {
	left.Processes = left.Processes || right.Processes
	left.IPv4 = left.IPv4 || right.IPv4
	left.IPv6 = left.IPv6 || right.IPv6
	left.Domains = left.Domains || right.Domains
	left.DomainMappings = left.DomainMappings || right.DomainMappings
	left.HotReload = left.HotReload || right.HotReload
	left.Rollback = left.Rollback || right.Rollback
	return left
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
