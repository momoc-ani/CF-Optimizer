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
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- s.server.Serve(serviceContext) }()
	schedulerErrors := make(chan error, 1)
	go func() { schedulerErrors <- s.schedule(serviceContext) }()
	discoveryErrors := make(chan error, 1)
	go func() { discoveryErrors <- s.observeDomains(serviceContext) }()
	select {
	case <-ctx.Done():
		<-serverErrors
		<-schedulerErrors
		<-discoveryErrors
		s.logger.Info("后台服务停止", "result", "context_cancelled")
		return nil
	case err := <-serverErrors:
		return fmt.Errorf("IPC server stopped: %w", err)
	case err := <-schedulerErrors:
		return fmt.Errorf("scheduler stopped: %w", err)
	case err := <-discoveryErrors:
		return fmt.Errorf("domain discovery stopped: %w", err)
	}
}

// observeDomains 按配置周期执行只读发现，并记录验证与策略刷新结果。
func (s *Service) observeDomains(ctx context.Context) error {
	initialConfig := s.runtime.View().Config
	if !initialConfig.Acceleration.Enabled || !initialConfig.Acceleration.AutoDiscover {
		<-ctx.Done()
		return nil
	}
	ticker := time.NewTicker(initialConfig.Acceleration.DiscoveryInterval.Duration())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
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
	initialConfig := s.runtime.View().Config
	if !initialConfig.Schedule.Enabled {
		<-ctx.Done()
		return nil
	}
	optimizationTimer := time.NewTimer(initialRunDelay)
	defer optimizationTimer.Stop()
	networkTicker := time.NewTicker(initialConfig.Schedule.NetworkPoll.Duration())
	defer networkTicker.Stop()
	fingerprint, _ := cfnetwork.NetworkFingerprint(ctx, initialConfig.Network.CommandTimeout.Duration())
	failureCount := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-optimizationTimer.C:
			err := s.runScheduled(ctx)
			currentConfig := s.runtime.View().Config
			delay := currentConfig.Schedule.Interval.Duration()
			if errors.Is(err, optimizer.ErrAlreadyRunning) {
				delay = minimumRetryDelay
			} else if err != nil {
				failureCount++
				delay = exponentialDelay(failureCount, currentConfig.Schedule.Interval.Duration())
				s.logger.Warn("计划优选失败，将按退避重试", "error", err, "retry_in", delay)
			} else {
				failureCount = 0
			}
			optimizationTimer.Reset(delay)
		case <-networkTicker.C:
			currentConfig := s.runtime.View().Config
			if !currentConfig.Schedule.RunOnNetworkChange {
				continue
			}
			current, err := cfnetwork.NetworkFingerprint(ctx, currentConfig.Network.CommandTimeout.Duration())
			if err != nil {
				s.logger.Warn("网络变化检测失败", "error", err)
				continue
			}
			if fingerprint != "" && current != fingerprint {
				if !optimizationTimer.Stop() {
					select {
					case <-optimizationTimer.C:
					default:
					}
				}
				optimizationTimer.Reset(networkChangeDebounce)
				s.logger.Info("检测到默认网络路径变化", "action", "schedule_retest")
			}
			fingerprint = current
		}
	}
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
