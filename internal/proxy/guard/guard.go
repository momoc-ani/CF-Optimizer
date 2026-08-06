package guard

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

// ErrMaintenanceBusy 表示优选或其他策略维护正在占用唯一执行权。
var ErrMaintenanceBusy = errors.New("policy maintenance is busy")

// ErrDesiredPolicyChanged 表示等待期间最后成功策略已经发生变化。
var ErrDesiredPolicyChanged = errors.New("desired policy changed")

// ActivityState 描述代理内核是否正在承载系统代理流量。
type ActivityState string

const (
	ActivityOffline  ActivityState = "offline"
	ActivityInactive ActivityState = "inactive"
	ActivityActive   ActivityState = "active"
	ActivityUnknown  ActivityState = "unknown"
)

// State 描述一个内核规则守护实例的可观察状态。
type State string

const (
	StateOffline   State = "offline"
	StateNoPolicy  State = "no_policy"
	StateStandby   State = "standby"
	StateChecking  State = "checking"
	StateDrifted   State = "drifted"
	StateRestoring State = "restoring"
	StateVerified  State = "verified"
	StateRetryWait State = "retry_wait"
	StateFailed    State = "failed"
)

// Observation 是策略对内核运行状态和代理活动状态的只读观察。
type Observation struct {
	Online            bool          `json:"online"`
	Activity          ActivityState `json:"activity"`
	SystemProxyActive bool          `json:"system_proxy_active"`
	TUNActive         bool          `json:"tun_active"`
	Manageable        bool          `json:"manageable"`
	Revision          string        `json:"revision,omitempty"`
	Endpoint          string        `json:"endpoint,omitempty"`
	ConfigPath        string        `json:"config_path,omitempty"`
	Message           string        `json:"message,omitempty"`
}

// DesiredPolicy 保存守护线程可恢复的最后一份已验证策略。
type DesiredPolicy struct {
	Revision  string
	Policy    proxy.DirectPolicy
	AppliedAt time.Time
}

// Inspection 描述活动配置与期望策略之间的差异。
type Inspection struct {
	Healthy      bool     `json:"healthy"`
	DriftReasons []string `json:"drift_reasons,omitempty"`
	Fingerprint  string   `json:"fingerprint,omitempty"`
}

// RepairPlan 是内核策略生成但尚未应用的版本化修复计划。
type RepairPlan struct {
	ID      string          `json:"id"`
	Target  string          `json:"target"`
	Summary []string        `json:"summary,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

// RepairReceipt 保存验证和回滚一次守护修复所需的策略私有数据。
type RepairReceipt struct {
	ID        string          `json:"id"`
	Target    string          `json:"target"`
	Changed   bool            `json:"changed"`
	AppliedAt time.Time       `json:"applied_at"`
	Payload   json.RawMessage `json:"payload"`
}

// Verification 保存严格连接验证证据以及失败时的处理建议。
type Verification struct {
	Verified            bool     `json:"verified"`
	Direct              bool     `json:"direct"`
	TargetAddresses     []string `json:"target_addresses,omitempty"`
	Interface           string   `json:"interface,omitempty"`
	Gateway             string   `json:"gateway,omitempty"`
	FailureKind         string   `json:"failure_kind,omitempty"`
	Message             string   `json:"message,omitempty"`
	RollbackRecommended bool     `json:"-"`
}

// Strategy 定义一个代理内核规则守护目标的完整安全生命周期。
type Strategy interface {
	ID() string
	Observe(context.Context) (Observation, error)
	Inspect(context.Context, DesiredPolicy) (Inspection, error)
	Plan(context.Context, DesiredPolicy, Inspection) (RepairPlan, error)
	Apply(context.Context, RepairPlan) (RepairReceipt, error)
	Verify(context.Context, DesiredPolicy, RepairReceipt) (Verification, error)
	Rollback(context.Context, RepairReceipt) error
}

// DesiredPolicySource 只读取最后一份已经验证并持久化的策略。
type DesiredPolicySource interface {
	CurrentDesiredPolicy(context.Context) (DesiredPolicy, bool, error)
}

// MaintenanceExecutor 将规则修复与完整优选、配置维护串行化。
type MaintenanceExecutor interface {
	TryExecute(context.Context, string, func(context.Context, DesiredPolicy) error) error
}

// StatusSink 接收去敏后的规则守护状态供 IPC 和界面读取。
type StatusSink interface {
	SetPolicyGuardStatus(Status)
}

// Status 是一个守护策略实例的精简运行状态。
type Status struct {
	ID                string        `json:"id"`
	State             State         `json:"state"`
	Online            bool          `json:"online"`
	Activity          ActivityState `json:"activity"`
	SystemProxyActive bool          `json:"system_proxy_active"`
	TUNActive         bool          `json:"tun_active"`
	Manageable        bool          `json:"manageable"`
	Endpoint          string        `json:"endpoint,omitempty"`
	ConfigPath        string        `json:"config_path,omitempty"`
	PolicyRevision    string        `json:"policy_revision,omitempty"`
	DriftReasons      []string      `json:"drift_reasons,omitempty"`
	LastCheckedAt     *time.Time    `json:"last_checked_at,omitempty"`
	LastVerifiedAt    *time.Time    `json:"last_verified_at,omitempty"`
	RetryAt           *time.Time    `json:"retry_at,omitempty"`
	Transition        uint64        `json:"transition"`
	Message           string        `json:"message,omitempty"`
}
