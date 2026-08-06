package guard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const defaultFailureThreshold = 4

// Options 定义通用守护调度间隔；生产默认值避免高频访问代理控制端。
type Options struct {
	OfflinePoll      time.Duration
	ActivePoll       time.Duration
	AuditInterval    time.Duration
	StableDelay      time.Duration
	RetryDelays      []time.Duration
	FailureThreshold int
}

// DefaultOptions 返回适用于本地代理内核的保守守护调度参数。
func DefaultOptions() Options {
	return Options{
		OfflinePoll: 5 * time.Second, ActivePoll: 2 * time.Second,
		AuditInterval: 30 * time.Second, StableDelay: time.Second,
		RetryDelays:      []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute},
		FailureThreshold: defaultFailureThreshold,
	}
}

// Supervisor 为每个内核策略运行独立状态机，并共享策略来源和维护执行器。
type Supervisor struct {
	strategies []Strategy
	source     DesiredPolicySource
	executor   MaintenanceExecutor
	sink       StatusSink
	logger     *slog.Logger
	options    Options
}

// NewSupervisor 创建不启动 goroutine 的规则守护调度器。
func NewSupervisor(strategies []Strategy, source DesiredPolicySource, executor MaintenanceExecutor, sink StatusSink, logger *slog.Logger, options Options) (*Supervisor, error) {
	if source == nil || executor == nil || sink == nil || logger == nil {
		return nil, errors.New("policy guard source, executor, status sink and logger are required")
	}
	seen := make(map[string]struct{}, len(strategies))
	for _, strategy := range strategies {
		if strategy == nil || strategy.ID() == "" {
			return nil, errors.New("policy guard strategy and ID are required")
		}
		if _, exists := seen[strategy.ID()]; exists {
			return nil, fmt.Errorf("duplicate policy guard strategy %q", strategy.ID())
		}
		seen[strategy.ID()] = struct{}{}
	}
	options = normalizeOptions(options)
	return &Supervisor{strategies: strategies, source: source, executor: executor, sink: sink, logger: logger.With("component", "policy_guard"), options: options}, nil
}

// Run 启动全部策略工作循环，直到上下文取消；单个策略故障不会终止后台服务。
func (s *Supervisor) Run(ctx context.Context) error {
	var workers sync.WaitGroup
	for _, strategy := range s.strategies {
		workers.Add(1)
		go func(strategy Strategy) {
			defer workers.Done()
			s.runStrategy(ctx, strategy)
		}(strategy)
	}
	<-ctx.Done()
	workers.Wait()
	return nil
}

func (s *Supervisor) runStrategy(ctx context.Context, strategy Strategy) {
	status := Status{ID: strategy.ID(), State: StateChecking, Activity: ActivityUnknown}
	s.publish(&status, StateChecking, "正在检查代理内核状态")
	var activeRevision string
	var verifiedPolicyRevision string
	var nextAudit time.Time
	var retryAt time.Time
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		desired, hasPolicy, policyErr := s.source.CurrentDesiredPolicy(ctx)
		if policyErr != nil {
			failures++
			retryAt = time.Now().Add(s.retryDelay(failures))
			s.publishFailure(&status, failures, retryAt, fmt.Sprintf("读取上一份策略失败: %v", policyErr))
			if !waitContext(ctx, s.options.ActivePoll) {
				return
			}
			continue
		}
		if !hasPolicy {
			activeRevision = ""
			verifiedPolicyRevision = ""
			failures = 0
			status.RetryAt = nil
			status.DriftReasons = nil
			s.publish(&status, StateNoPolicy, "没有可供恢复的已验证策略")
			if !waitContext(ctx, s.options.OfflinePoll) {
				return
			}
			continue
		}

		observation, observeErr := strategy.Observe(ctx)
		if observeErr != nil {
			failures++
			retryAt = time.Now().Add(s.retryDelay(failures))
			s.publishFailure(&status, failures, retryAt, fmt.Sprintf("代理状态检测失败: %v", observeErr))
			if !waitContext(ctx, s.options.OfflinePoll) {
				return
			}
			continue
		}
		copyObservation(&status, observation)
		status.PolicyRevision = desired.Revision
		if !observation.Online || observation.Activity == ActivityOffline {
			activeRevision = ""
			verifiedPolicyRevision = ""
			failures = 0
			status.RetryAt = nil
			status.DriftReasons = nil
			s.publish(&status, StateOffline, nonEmpty(observation.Message, "代理内核未运行"))
			if !waitContext(ctx, s.options.OfflinePoll) {
				return
			}
			continue
		}
		if observation.Activity != ActivityActive {
			activeRevision = ""
			verifiedPolicyRevision = ""
			failures = 0
			status.RetryAt = nil
			status.DriftReasons = nil
			s.publish(&status, StateStandby, nonEmpty(observation.Message, "系统代理和 TUN 均未启用"))
			if !waitContext(ctx, s.options.ActivePoll) {
				return
			}
			continue
		}
		if !observation.Manageable {
			failures++
			retryAt = time.Now().Add(s.retryDelay(failures))
			s.publishFailure(&status, failures, retryAt, "代理内核在线，但没有可安全管理的活动配置")
			if !waitContext(ctx, s.options.ActivePoll) {
				return
			}
			continue
		}

		activationRevision := observation.Revision + "\x00" + desired.Revision
		if activeRevision != activationRevision {
			activeRevision = activationRevision
			verifiedPolicyRevision = ""
			nextAudit = time.Time{}
			s.publish(&status, StateChecking, "代理已启用，正在等待活动配置稳定")
			if !waitContext(ctx, s.options.StableDelay) {
				return
			}
			continue
		}
		now := time.Now()
		if !retryAt.IsZero() && now.Before(retryAt) {
			retryCopy := retryAt.UTC()
			status.RetryAt = &retryCopy
			if !waitContext(ctx, s.options.ActivePoll) {
				return
			}
			continue
		}
		if verifiedPolicyRevision == desired.Revision && !nextAudit.IsZero() && now.Before(nextAudit) {
			if !waitContext(ctx, s.options.ActivePoll) {
				return
			}
			continue
		}

		s.publish(&status, StateChecking, "正在检查活动规则")
		inspection, inspectErr := strategy.Inspect(ctx, desired)
		checkedAt := time.Now().UTC()
		status.LastCheckedAt = &checkedAt
		if inspectErr != nil {
			failures++
			retryAt = time.Now().Add(s.retryDelay(failures))
			s.publishFailure(&status, failures, retryAt, fmt.Sprintf("检查活动规则失败: %v", inspectErr))
			continue
		}
		status.DriftReasons = append([]string(nil), inspection.DriftReasons...)
		if inspection.Healthy {
			if verifiedPolicyRevision == desired.Revision {
				nextAudit = time.Now().Add(s.options.AuditInterval)
				failures = 0
				retryAt = time.Time{}
				status.RetryAt = nil
				s.publish(&status, StateVerified, "活动规则审计通过")
				continue
			}
			verification, verifyErr := strategy.Verify(ctx, desired, RepairReceipt{})
			if verifyErr != nil || !verification.Verified {
				failures++
				retryAt = time.Now().Add(s.retryDelay(failures))
				s.publishFailure(&status, failures, retryAt, verificationMessage(verification, verifyErr))
				continue
			}
			s.markVerified(&status, desired, verification)
			verifiedPolicyRevision = desired.Revision
			nextAudit = time.Now().Add(s.options.AuditInterval)
			failures = 0
			retryAt = time.Time{}
			continue
		}

		s.publish(&status, StateDrifted, "检测到代理规则被重载或覆盖")
		var repairedVerification Verification
		repairErr := s.executor.TryExecute(ctx, desired.Revision, func(maintenanceContext context.Context, lockedDesired DesiredPolicy) error {
			freshInspection, err := strategy.Inspect(maintenanceContext, lockedDesired)
			if err != nil {
				return err
			}
			if freshInspection.Healthy {
				repairedVerification, err = strategy.Verify(maintenanceContext, lockedDesired, RepairReceipt{})
				if err != nil {
					return err
				}
				if !repairedVerification.Verified {
					return errors.New(nonEmpty(repairedVerification.Message, "rule verification failed"))
				}
				return nil
			}
			s.publish(&status, StateRestoring, "正在恢复上一份已验证规则")
			plan, err := strategy.Plan(maintenanceContext, lockedDesired, freshInspection)
			if err != nil {
				return fmt.Errorf("plan rule repair: %w", err)
			}
			receipt, err := strategy.Apply(maintenanceContext, plan)
			if err != nil {
				return fmt.Errorf("apply rule repair: %w", err)
			}
			repairedVerification, err = strategy.Verify(maintenanceContext, lockedDesired, receipt)
			if err == nil && repairedVerification.Verified {
				return nil
			}
			if receipt.Changed && repairedVerification.RollbackRecommended {
				if rollbackErr := strategy.Rollback(context.WithoutCancel(maintenanceContext), receipt); rollbackErr != nil {
					return errors.Join(fmt.Errorf("verify rule repair: %w", err), fmt.Errorf("rollback rule repair: %w", rollbackErr))
				}
			}
			if err != nil {
				return fmt.Errorf("verify rule repair: %w", err)
			}
			return errors.New(nonEmpty(repairedVerification.Message, "rule repair verification failed"))
		})
		if repairErr != nil {
			if errors.Is(repairErr, ErrDesiredPolicyChanged) {
				activeRevision = ""
				verifiedPolicyRevision = ""
				retryAt = time.Time{}
				continue
			}
			failures++
			retryAt = time.Now().Add(s.retryDelay(failures))
			message := fmt.Sprintf("恢复代理规则失败: %v", repairErr)
			if errors.Is(repairErr, ErrMaintenanceBusy) {
				message = "优选或其他维护正在运行，规则恢复已延后"
			}
			s.publishFailure(&status, failures, retryAt, message)
			continue
		}
		failures = 0
		retryAt = time.Time{}
		verifiedPolicyRevision = desired.Revision
		nextAudit = time.Now().Add(s.options.AuditInterval)
		s.markVerified(&status, desired, repairedVerification)
		if !waitContext(ctx, s.options.ActivePoll) {
			return
		}
	}
}

func (s *Supervisor) markVerified(status *Status, desired DesiredPolicy, verification Verification) {
	verifiedAt := time.Now().UTC()
	status.LastVerifiedAt = &verifiedAt
	status.RetryAt = nil
	status.DriftReasons = nil
	status.PolicyRevision = desired.Revision
	message := nonEmpty(verification.Message, "代理规则和实际 DIRECT 连接已验证")
	s.publish(status, StateVerified, message)
}

func (s *Supervisor) publishFailure(status *Status, failures int, retryAt time.Time, message string) {
	retryCopy := retryAt.UTC()
	status.RetryAt = &retryCopy
	state := StateRetryWait
	if failures >= s.options.FailureThreshold {
		state = StateFailed
	}
	s.publish(status, state, message)
}

func (s *Supervisor) publish(status *Status, state State, message string) {
	changed := status.State != state || status.Message != message
	if changed {
		status.Transition++
	}
	status.State = state
	status.Message = message
	clone := *status
	clone.DriftReasons = append([]string(nil), status.DriftReasons...)
	s.sink.SetPolicyGuardStatus(clone)
	if !changed {
		return
	}
	level := slog.LevelDebug
	if state == StateDrifted || state == StateRetryWait || state == StateFailed {
		level = slog.LevelWarn
	} else if state == StateRestoring || state == StateVerified {
		level = slog.LevelInfo
	}
	s.logger.Log(context.Background(), level, "代理规则守护状态变化", "adapter", status.ID, "state", state, "result", message)
}

func (s *Supervisor) retryDelay(failures int) time.Duration {
	index := failures - 1
	if index < 0 {
		index = 0
	}
	if index >= len(s.options.RetryDelays) {
		index = len(s.options.RetryDelays) - 1
	}
	return s.options.RetryDelays[index]
}

func normalizeOptions(options Options) Options {
	defaults := DefaultOptions()
	if options.OfflinePoll <= 0 {
		options.OfflinePoll = defaults.OfflinePoll
	}
	if options.ActivePoll <= 0 {
		options.ActivePoll = defaults.ActivePoll
	}
	if options.AuditInterval <= 0 {
		options.AuditInterval = defaults.AuditInterval
	}
	if options.StableDelay <= 0 {
		options.StableDelay = defaults.StableDelay
	}
	if len(options.RetryDelays) == 0 {
		options.RetryDelays = defaults.RetryDelays
	}
	if options.FailureThreshold < 1 {
		options.FailureThreshold = defaults.FailureThreshold
	}
	return options
}

func copyObservation(status *Status, observation Observation) {
	status.Online = observation.Online
	status.Activity = observation.Activity
	status.SystemProxyActive = observation.SystemProxyActive
	status.TUNActive = observation.TUNActive
	status.Manageable = observation.Manageable
	status.Endpoint = observation.Endpoint
	status.ConfigPath = observation.ConfigPath
}

func verificationMessage(verification Verification, err error) string {
	if err != nil {
		return fmt.Sprintf("严格连接验证失败: %v", err)
	}
	return nonEmpty(verification.Message, "严格连接验证失败")
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
