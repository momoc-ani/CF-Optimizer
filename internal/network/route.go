package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
)

const routeJournalVersion = 1

// ErrRouteNotFound 表示目标前缀不存在精确路由记录。
var ErrRouteNotFound = errors.New("route not found")

// RouteSpec 是经过服务端校验后交给平台后端的路由定义。
type RouteSpec struct {
	Prefix         string `json:"prefix"`
	Gateway        string `json:"gateway"`
	Interface      string `json:"interface"`
	InterfaceIndex int    `json:"interface_index,omitempty"`
	Metric         int    `json:"metric"`
}

// Validate 拒绝默认路由、无效网关和缺失接口，防止扩大修改范围。
func (r RouteSpec) Validate() error {
	prefix, err := netip.ParsePrefix(r.Prefix)
	if err != nil {
		return fmt.Errorf("parse route prefix: %w", err)
	}
	if prefix.Bits() == 0 || !prefix.Addr().IsGlobalUnicast() {
		return errors.New("route prefix must be a non-default global unicast range")
	}
	gateway, err := netip.ParseAddr(r.Gateway)
	if err != nil {
		return fmt.Errorf("parse route gateway: %w", err)
	}
	if gateway.Is4() != prefix.Addr().Is4() || gateway.IsUnspecified() || gateway.IsMulticast() {
		return errors.New("route gateway and prefix must use the same address family")
	}
	if r.Interface == "" && r.InterfaceIndex <= 0 {
		return errors.New("route interface or interface index is required")
	}
	if r.Metric < 0 || r.Metric > 9999 {
		return errors.New("route metric must be between 0 and 9999")
	}
	return nil
}

// ResolvedRoute 描述系统查询到的实际路由和源地址。
type ResolvedRoute struct {
	RouteSpec
	SourceAddress string `json:"source_address,omitempty"`
}

// RouteBackend 隔离平台路由 API，便于事务测试使用无副作用替身。
type RouteBackend interface {
	Replace(context.Context, RouteSpec) error
	Delete(context.Context, RouteSpec) error
	Get(context.Context, string) (RouteSpec, error)
	Resolve(context.Context, netip.Addr) (ResolvedRoute, error)
}

// ChangePlan 显示路由变更的目标、原值和可回滚信息。
type ChangePlan struct {
	Route       RouteSpec  `json:"route"`
	Previous    *RouteSpec `json:"previous,omitempty"`
	Temporary   bool       `json:"temporary"`
	WillReplace bool       `json:"will_replace"`
}

// Transaction 记录一次路由修改的计划、验证和回滚状态。
type Transaction struct {
	ID           string         `json:"id"`
	Operation    string         `json:"operation"`
	Route        RouteSpec      `json:"route"`
	Previous     *RouteSpec     `json:"previous,omitempty"`
	Temporary    bool           `json:"temporary"`
	State        string         `json:"state"`
	StartedAt    time.Time      `json:"started_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	Verification *ResolvedRoute `json:"verification,omitempty"`
	Error        string         `json:"error,omitempty"`
}

type routeJournal struct {
	Version      int           `json:"version"`
	Transactions []Transaction `json:"transactions"`
}

// RouteController 以持久化事务协调平台路由修改、验证和崩溃恢复。
type RouteController struct {
	backend RouteBackend
	path    string
	enabled bool
	logger  *slog.Logger
	now     func() time.Time
	mu      sync.Mutex
	journal routeJournal
}

// NewRouteController 创建路由事务控制器并加载尚未恢复的操作日志。
func NewRouteController(dataDir string, backend RouteBackend, enabled bool, logger *slog.Logger) (*RouteController, error) {
	if backend == nil {
		return nil, errors.New("route backend is required")
	}
	if logger == nil {
		return nil, errors.New("route logger is required")
	}
	controller := &RouteController{
		backend: backend, path: filepath.Join(dataDir, "route-transactions.json"), enabled: enabled,
		logger: logger.With("component", "route"), now: time.Now,
		journal: routeJournal{Version: routeJournalVersion, Transactions: []Transaction{}},
	}
	if err := controller.load(); err != nil {
		return nil, err
	}
	return controller, nil
}

// Plan 查询目标前缀现状，并返回不产生系统修改的变更计划。
func (c *RouteController) Plan(ctx context.Context, route RouteSpec, temporary bool) (ChangePlan, error) {
	if err := route.Validate(); err != nil {
		return ChangePlan{}, err
	}
	plan := ChangePlan{Route: route, Temporary: temporary}
	previous, err := c.backend.Get(ctx, route.Prefix)
	if err == nil {
		plan.Previous = &previous
		plan.WillReplace = previous != route
	} else if !errors.Is(err, ErrRouteNotFound) {
		return ChangePlan{}, fmt.Errorf("query existing route: %w", err)
	}
	return plan, nil
}

// Apply 应用路由并查询实际选路；验证失败时自动恢复原路由。
func (c *RouteController) Apply(ctx context.Context, plan ChangePlan) (Transaction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return Transaction{}, errors.New("route management is disabled")
	}
	if err := plan.Route.Validate(); err != nil {
		return Transaction{}, err
	}
	transaction := Transaction{
		ID: newTransactionID(), Operation: "replace", Route: plan.Route, Previous: plan.Previous,
		Temporary: plan.Temporary, State: "planned", StartedAt: c.now().UTC(), UpdatedAt: c.now().UTC(),
	}
	if err := c.appendTransaction(transaction); err != nil {
		return Transaction{}, err
	}
	c.logPhase(transaction, "plan", "started", nil)
	if err := c.backend.Replace(ctx, plan.Route); err != nil {
		operationErr := fmt.Errorf("apply route: %w", err)
		rollbackErr := c.rollback(context.WithoutCancel(ctx), &transaction)
		transaction.State = "rolled_back"
		if rollbackErr != nil {
			transaction.State = "rollback_failed"
			operationErr = errors.Join(operationErr, fmt.Errorf("rollback failed route: %w", rollbackErr))
		}
		transaction.Error = operationErr.Error()
		_ = c.replaceTransaction(transaction)
		c.logPhase(transaction, "apply", "failed", operationErr)
		return transaction, operationErr
	}
	transaction.State = "applied"
	if err := c.replaceTransaction(transaction); err != nil {
		_ = c.rollback(context.WithoutCancel(ctx), &transaction)
		return transaction, err
	}
	c.logPhase(transaction, "apply", "completed", nil)
	resolved, err := c.verify(ctx, plan.Route)
	if err != nil {
		transaction.Error = err.Error()
		if rollbackErr := c.rollback(context.WithoutCancel(ctx), &transaction); rollbackErr != nil {
			transaction.Error += "; rollback: " + rollbackErr.Error()
		}
		transaction.State = "rolled_back"
		_ = c.replaceTransaction(transaction)
		c.logPhase(transaction, "verify", "failed", err)
		return transaction, err
	}
	transaction.State = "verified"
	transaction.Verification = &resolved
	transaction.UpdatedAt = c.now().UTC()
	if err := c.replaceTransaction(transaction); err != nil {
		return transaction, err
	}
	c.logPhase(transaction, "verify", "completed", nil)
	return transaction, nil
}

// Remove 删除精确前缀路由并确认目标记录已经消失。
func (c *RouteController) Remove(ctx context.Context, route RouteSpec) (Transaction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return Transaction{}, errors.New("route management is disabled")
	}
	if err := route.Validate(); err != nil {
		return Transaction{}, err
	}
	transaction := Transaction{ID: newTransactionID(), Operation: "delete", Route: route, State: "planned", StartedAt: c.now().UTC(), UpdatedAt: c.now().UTC()}
	if previous, err := c.backend.Get(ctx, route.Prefix); err == nil {
		transaction.Previous = &previous
	} else if errors.Is(err, ErrRouteNotFound) {
		transaction.State = "verified"
		return transaction, nil
	} else {
		return transaction, err
	}
	if err := c.appendTransaction(transaction); err != nil {
		return Transaction{}, err
	}
	c.logPhase(transaction, "plan", "started", nil)
	if err := c.backend.Delete(ctx, route); err != nil && !errors.Is(err, ErrRouteNotFound) {
		return c.failTransaction(transaction.ID, "apply_failed", fmt.Errorf("delete route: %w", err))
	}
	transaction.State = "applied"
	if err := c.replaceTransaction(transaction); err != nil {
		return transaction, err
	}
	if _, err := c.backend.Get(ctx, route.Prefix); !errors.Is(err, ErrRouteNotFound) {
		if err == nil {
			err = errors.New("route still exists after deletion")
		}
		if transaction.Previous != nil {
			_ = c.backend.Replace(ctx, *transaction.Previous)
		}
		return c.failTransaction(transaction.ID, "rolled_back", err)
	}
	transaction.State = "verified"
	transaction.UpdatedAt = c.now().UTC()
	if err := c.replaceTransaction(transaction); err != nil {
		return transaction, err
	}
	c.logPhase(transaction, "verify", "completed", nil)
	return transaction, nil
}

// Recover 清理未完成和临时路由，并恢复修改前的精确路由。
func (c *RouteController) Recover(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return nil
	}
	var recoveredErrors []error
	for index := range c.journal.Transactions {
		transaction := &c.journal.Transactions[index]
		needsRecovery := transaction.State == "planned" || transaction.State == "applied" || transaction.State == "rollback_failed" || transaction.State == "recovery_failed" || (transaction.Temporary && transaction.State == "verified")
		if !needsRecovery {
			continue
		}
		if err := c.rollback(ctx, transaction); err != nil {
			transaction.State = "recovery_failed"
			transaction.Error = err.Error()
			recoveredErrors = append(recoveredErrors, fmt.Errorf("transaction %s: %w", transaction.ID, err))
			c.logPhase(*transaction, "rollback", "failed", err)
		} else {
			transaction.State = "recovered"
			transaction.Error = ""
			c.logPhase(*transaction, "rollback", "completed", nil)
		}
		transaction.UpdatedAt = c.now().UTC()
	}
	if err := c.persist(); err != nil {
		recoveredErrors = append(recoveredErrors, err)
	}
	return errors.Join(recoveredErrors...)
}

// Rollback 按事务 ID 恢复修改前路由，并记录验证后的回滚状态。
func (c *RouteController) Rollback(ctx context.Context, transactionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := range c.journal.Transactions {
		transaction := &c.journal.Transactions[index]
		if transaction.ID != transactionID {
			continue
		}
		if transaction.State == "rolled_back" || transaction.State == "recovered" {
			return nil
		}
		if err := c.rollback(ctx, transaction); err != nil {
			transaction.State = "rollback_failed"
			transaction.Error = err.Error()
			transaction.UpdatedAt = c.now().UTC()
			_ = c.persist()
			return err
		}
		transaction.State = "rolled_back"
		transaction.Error = ""
		transaction.UpdatedAt = c.now().UTC()
		if err := c.persist(); err != nil {
			return err
		}
		c.logPhase(*transaction, "rollback", "completed", nil)
		return nil
	}
	return fmt.Errorf("route transaction %s is missing", transactionID)
}

// Transactions 返回路由审计记录的副本。
func (c *RouteController) Transactions() []Transaction {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]Transaction, len(c.journal.Transactions))
	copy(result, c.journal.Transactions)
	return result
}

func (c *RouteController) verify(ctx context.Context, expected RouteSpec) (ResolvedRoute, error) {
	prefix := netip.MustParsePrefix(expected.Prefix)
	target := prefix.Addr()
	if prefix.Addr().Is4() && prefix.Bits() < 32 {
		target = target.Next()
	} else if prefix.Addr().Is6() && prefix.Bits() < 128 {
		target = target.Next()
	}
	resolved, err := c.backend.Resolve(ctx, target)
	if err != nil {
		return ResolvedRoute{}, fmt.Errorf("resolve applied route: %w", err)
	}
	if resolved.Interface != expected.Interface && (expected.InterfaceIndex == 0 || resolved.InterfaceIndex != expected.InterfaceIndex) {
		return ResolvedRoute{}, fmt.Errorf("route uses interface %q instead of %q", resolved.Interface, expected.Interface)
	}
	if resolved.Gateway != expected.Gateway {
		return ResolvedRoute{}, fmt.Errorf("route uses gateway %q instead of %q", resolved.Gateway, expected.Gateway)
	}
	return resolved, nil
}

func (c *RouteController) rollback(ctx context.Context, transaction *Transaction) error {
	c.logPhase(*transaction, "rollback", "started", nil)
	if transaction.Previous != nil {
		return c.backend.Replace(ctx, *transaction.Previous)
	}
	if err := c.backend.Delete(ctx, transaction.Route); err != nil && !errors.Is(err, ErrRouteNotFound) {
		return err
	}
	return nil
}

func (c *RouteController) load() error {
	data, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read route journal: %w", err)
	}
	if err := json.Unmarshal(data, &c.journal); err != nil {
		return fmt.Errorf("decode route journal: %w", err)
	}
	if c.journal.Version != routeJournalVersion {
		return fmt.Errorf("unsupported route journal version %d", c.journal.Version)
	}
	return nil
}

func (c *RouteController) appendTransaction(transaction Transaction) error {
	c.journal.Transactions = append(c.journal.Transactions, transaction)
	return c.persist()
}

func (c *RouteController) replaceTransaction(transaction Transaction) error {
	transaction.UpdatedAt = c.now().UTC()
	for index := range c.journal.Transactions {
		if c.journal.Transactions[index].ID == transaction.ID {
			c.journal.Transactions[index] = transaction
			return c.persist()
		}
	}
	return fmt.Errorf("route transaction %s is missing", transaction.ID)
}

func (c *RouteController) failTransaction(id, state string, operationErr error) (Transaction, error) {
	for index := range c.journal.Transactions {
		if c.journal.Transactions[index].ID != id {
			continue
		}
		transaction := c.journal.Transactions[index]
		transaction.State = state
		transaction.Error = operationErr.Error()
		transaction.UpdatedAt = c.now().UTC()
		_ = c.replaceTransaction(transaction)
		c.logPhase(transaction, "apply", "failed", operationErr)
		return transaction, operationErr
	}
	return Transaction{}, operationErr
}

func (c *RouteController) persist() error {
	data, err := json.MarshalIndent(c.journal, "", "  ")
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(c.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("persist route journal: %w", err)
	}
	return nil
}

func (c *RouteController) logPhase(transaction Transaction, phase, result string, operationErr error) {
	attributes := []any{
		"transaction_id", transaction.ID, "phase", phase, "result", result,
		"target_ip", transaction.Route.Prefix, "interface", transaction.Route.Interface,
	}
	if operationErr != nil {
		attributes = append(attributes, "error", operationErr)
		c.logger.Error("路由事务阶段失败", attributes...)
		return
	}
	c.logger.Info("路由事务阶段完成", attributes...)
}

func newTransactionID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("route-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

// NewPlatformRouteBackend 创建当前平台的路由后端。
func NewPlatformRouteBackend(commandTimeout time.Duration) RouteBackend {
	return newPlatformRouteBackend(commandTimeout)
}
