package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/application"
	"github.com/cf-optimizer/cf-optimizer/internal/ipc"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/optimizer"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/guard"
)

const (
	initialRunDelay       = 2 * time.Second
	networkChangeDebounce = 10 * time.Second
	minimumRetryDelay     = time.Minute
)

// Service 同时运行受限 IPC 和周期/网络变化优选调度器。
type Service struct {
	runtime *application.Runtime
	api     *application.API
	server  *ipc.Server
	logger  *slog.Logger
}

// New 创建后台服务，不启动监听或任务。
func New(runtime *application.Runtime, api *application.API) (*Service, error) {
	if runtime == nil || api == nil {
		return nil, errors.New("daemon runtime and API are required")
	}
	view := runtime.View()
	server, err := ipc.NewServer(view.Config.IPC.Endpoint, api, runtime.Logger)
	if err != nil {
		return nil, err
	}
	return &Service{runtime: runtime, api: api, server: server, logger: runtime.Logger.With("component", "daemon")}, nil
}

// Run 恢复未完成路由事务并运行 IPC 与调度循环，直到上下文取消。
func (s *Service) Run(ctx context.Context) error {
	serviceContext, cancel := context.WithCancel(ctx)
	defer cancel()
	s.logger.Info("后台服务启动", "platform", runtime.GOOS, "endpoint", s.runtime.View().Config.IPC.Endpoint)
	if err := s.runtime.Routes.Recover(serviceContext); err != nil {
		return fmt.Errorf("recover route transactions: %w", err)
	}
	if err := s.runtime.RecoverPendingPolicy(serviceContext); err != nil {
		return fmt.Errorf("recover pending policy transaction: %w", err)
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- s.server.Serve(serviceContext) }()
	schedulerErrors := make(chan error, 1)
	go func() { schedulerErrors <- s.schedule(serviceContext) }()
	discoveryErrors := make(chan error, 1)
	go func() { discoveryErrors <- s.observeDomains(serviceContext) }()
	guardErrors := make(chan error, 1)
	go func() { guardErrors <- s.guardPolicies(serviceContext) }()
	select {
	case <-ctx.Done():
		<-serverErrors
		<-schedulerErrors
		<-discoveryErrors
		<-guardErrors
		s.logger.Info("后台服务停止", "result", "context_cancelled")
		return nil
	case err := <-serverErrors:
		return fmt.Errorf("IPC server stopped: %w", err)
	case err := <-schedulerErrors:
		return fmt.Errorf("scheduler stopped: %w", err)
	case err := <-discoveryErrors:
		return fmt.Errorf("domain discovery stopped: %w", err)
	case err := <-guardErrors:
		return fmt.Errorf("policy guard stopped: %w", err)
	}
}

// guardPolicies 在运行配置切换时重建内核策略实例，不让旧端点继续写入。
func (s *Service) guardPolicies(ctx context.Context) error {
	for {
		strategies, err := s.runtime.RuleGuardStrategies()
		if err != nil {
			return fmt.Errorf("build policy guard strategies: %w", err)
		}
		s.api.ResetPolicyGuardStatuses()
		supervisor, err := guard.NewSupervisor(strategies, s.runtime, s.runtime, s.api, s.logger, guard.DefaultOptions())
		if err != nil {
			return err
		}
		guardContext, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- supervisor.Run(guardContext) }()
		configChanges := s.runtime.ConfigChanges()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case <-configChanges:
			cancel()
			<-done
			continue
		case err := <-done:
			cancel()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

// observeDomains 按配置周期执行只读发现，并记录验证与策略刷新结果。
func (s *Service) observeDomains(ctx context.Context) error {
	for {
		currentConfig := s.runtime.View().Config
		configChanges := s.runtime.ConfigChanges()
		if !currentConfig.Acceleration.Enabled || !currentConfig.Acceleration.AutoDiscover {
			select {
			case <-ctx.Done():
				return nil
			case <-configChanges:
				continue
			}
		}
		timer := time.NewTimer(currentConfig.Acceleration.DiscoveryInterval.Duration())
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return nil
		case <-configChanges:
			stopTimer(timer)
			continue
		case <-timer.C:
			result, err := s.runtime.DiscoverAccelerationDomains(ctx)
			if err != nil {
				s.logger.Debug("自动发现加速域名未完成", "component", "acceleration", "error", err)
				continue
			}
			if result.Verified > 0 || result.Activated > 0 {
				s.logger.Info("自动发现加速域名完成", "component", "acceleration", "observed", result.Observed, "verified", result.Verified, "activated", result.Activated, "policy_refreshed", result.PolicyRefreshed)
			}
		}
	}
}

func (s *Service) schedule(ctx context.Context) error {
	var optimizationTimer *time.Timer
	var optimizationChannel <-chan time.Time
	var networkTimer *time.Timer
	var networkChannel <-chan time.Time
	defer func() {
		stopTimer(optimizationTimer)
		stopTimer(networkTimer)
	}()
	resetOptimization := func(delay time.Duration) {
		resetTimer(&optimizationTimer, delay)
		optimizationChannel = optimizationTimer.C
	}
	resetNetworkPoll := func(delay time.Duration) {
		resetTimer(&networkTimer, delay)
		networkChannel = networkTimer.C
	}
	disableTimers := func() {
		stopTimer(optimizationTimer)
		stopTimer(networkTimer)
		optimizationChannel = nil
		networkChannel = nil
	}
	currentConfig := s.runtime.View().Config
	configChanges := s.runtime.ConfigChanges()
	fingerprint, _ := cfnetwork.NetworkFingerprint(ctx, currentConfig.Network.CommandTimeout.Duration())
	if currentConfig.Schedule.Enabled {
		resetOptimization(initialRunDelay)
		resetNetworkPoll(currentConfig.Schedule.NetworkPoll.Duration())
		s.setScheduleStatus(true, currentConfig.Schedule.Interval.Duration(), time.Now().UTC().Add(initialRunDelay), "startup")
	} else {
		disableTimers()
		s.setScheduleStatus(false, currentConfig.Schedule.Interval.Duration(), time.Time{}, "disabled")
	}
	failureCount := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-configChanges:
			configChanges = s.runtime.ConfigChanges()
			currentConfig = s.runtime.View().Config
			fingerprint, _ = cfnetwork.NetworkFingerprint(ctx, currentConfig.Network.CommandTimeout.Duration())
			failureCount = 0
			if !currentConfig.Schedule.Enabled {
				disableTimers()
				s.setScheduleStatus(false, currentConfig.Schedule.Interval.Duration(), time.Time{}, "disabled")
				continue
			}
			resetOptimization(currentConfig.Schedule.Interval.Duration())
			resetNetworkPoll(currentConfig.Schedule.NetworkPoll.Duration())
			s.setScheduleStatus(true, currentConfig.Schedule.Interval.Duration(), time.Now().UTC().Add(currentConfig.Schedule.Interval.Duration()), "interval")
		case <-optimizationChannel:
			optimizationChannel = nil
			s.setScheduleStatus(true, s.runtime.View().Config.Schedule.Interval.Duration(), time.Time{}, "running")
			err := s.runScheduled(ctx)
			currentConfig := s.runtime.View().Config
			delay := currentConfig.Schedule.Interval.Duration()
			trigger := "interval"
			if errors.Is(err, optimizer.ErrAlreadyRunning) {
				delay = minimumRetryDelay
				trigger = "retry"
			} else if err != nil {
				failureCount++
				delay = exponentialDelay(failureCount, currentConfig.Schedule.Interval.Duration())
				trigger = "retry"
				s.logger.Warn("计划优选失败，将按退避重试", "error", err, "retry_in", delay)
			} else {
				failureCount = 0
			}
			resetOptimization(delay)
			s.setScheduleStatus(true, currentConfig.Schedule.Interval.Duration(), time.Now().UTC().Add(delay), trigger)
		case <-networkChannel:
			networkChannel = nil
			currentConfig := s.runtime.View().Config
			resetNetworkPoll(currentConfig.Schedule.NetworkPoll.Duration())
			if !currentConfig.Schedule.RunOnNetworkChange {
				continue
			}
			current, err := cfnetwork.NetworkFingerprint(ctx, currentConfig.Network.CommandTimeout.Duration())
			if err != nil {
				s.logger.Warn("网络变化检测失败", "error", err)
				continue
			}
			if fingerprint != "" && current != fingerprint {
				resetOptimization(networkChangeDebounce)
				s.setScheduleStatus(true, currentConfig.Schedule.Interval.Duration(), time.Now().UTC().Add(networkChangeDebounce), "network_change")
				s.logger.Info("检测到默认网络路径变化", "action", "schedule_retest")
			}
			fingerprint = current
		}
	}
}

// resetTimer 安全复用计时器，并清空可能残留的到期信号。
func resetTimer(timer **time.Timer, delay time.Duration) {
	if *timer == nil {
		*timer = time.NewTimer(delay)
		return
	}
	stopTimer(*timer)
	(*timer).Reset(delay)
}

// stopTimer 停止计时器并排空已经投递的信号。
func stopTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

// setScheduleStatus 将真实计时器承诺同步到只读状态接口。
func (s *Service) setScheduleStatus(enabled bool, interval time.Duration, next time.Time, trigger string) {
	status := application.ScheduleStatus{Enabled: enabled, Interval: interval.String(), Trigger: trigger}
	if !next.IsZero() {
		next = next.UTC()
		status.NextScheduledAt = &next
	}
	s.api.SetScheduleStatus(status)
}

func (s *Service) runScheduled(ctx context.Context) error {
	applyPolicy := s.runtime.View().ProxyCoordinator != nil
	_, err := s.api.RunOptimization(ctx, optimizer.RunOptions{ApplyPolicy: applyPolicy}, nil)
	return err
}

func exponentialDelay(failures int, maximum time.Duration) time.Duration {
	if maximum <= minimumRetryDelay {
		return maximum
	}
	delay := minimumRetryDelay
	for index := 1; index < failures; index++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}
