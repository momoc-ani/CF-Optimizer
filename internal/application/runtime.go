package application

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/cf-optimizer/cf-optimizer/internal/benchmark"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/optimizer"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/external"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/generic"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/hosts"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/mihomo"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/singbox"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/xray"
	"github.com/cf-optimizer/cf-optimizer/internal/ranges"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

const (
	cleanupAdapterGeneric  = "generic-route"
	cleanupAdapterMihomo   = "mihomo"
	cleanupAdapterSingBox  = "sing-box"
	cleanupAdapterXray     = "xray"
	cleanupAdapterExternal = "external"
	cleanupAdapterHosts    = "windows-hosts"
)

// Runtime 汇总一个进程共享的核心组件和已验证依赖关系。
type Runtime struct {
	mutex            sync.RWMutex
	Config           config.Config
	ConfigPath       string
	Store            *store.Store
	Ranges           *ranges.Catalog
	Runner           *optimizer.Runner
	Routes           *cfnetwork.RouteController
	RouteBackend     cfnetwork.RouteBackend
	PhysicalPath     cfnetwork.PhysicalPath
	DirectDial       cfnetwork.DialContextFunc
	ProxyCoordinator *proxy.Coordinator
	Logger           *slog.Logger
}

// RuntimeView 提供可并发读取的当前运行配置和执行依赖快照。
type RuntimeView struct {
	Config           config.Config
	Runner           *optimizer.Runner
	PhysicalPath     cfnetwork.PhysicalPath
	DirectDial       cfnetwork.DialContextFunc
	ProxyCoordinator *proxy.Coordinator
}

// RuntimeSession 描述一次快速流程使用且可在验证后激活的完整运行会话。
type RuntimeSession struct {
	Config           config.Config
	Runner           *optimizer.Runner
	PhysicalPath     cfnetwork.PhysicalPath
	DirectDial       cfnetwork.DialContextFunc
	ProxyCoordinator *proxy.Coordinator
}

// Build 创建后台服务和直接 CLI 共用的运行时，不执行任何网络修改。
func Build(cfg config.Config, configPath string, logger *slog.Logger) (*Runtime, error) {
	if logger == nil {
		return nil, fmt.Errorf("application logger is required")
	}
	stateStore, err := store.Open(cfg.DataDir, cfg.History.MaxRuns)
	if err != nil {
		return nil, err
	}
	physicalPath, pathErr := cfnetwork.DiscoverPhysicalPath(
		context.Background(), cfg.Network.Interface, cfg.Network.GatewayIPv4, cfg.Network.GatewayIPv6, cfg.Network.CommandTimeout.Duration(),
	)
	if pathErr != nil {
		if cfg.Network.ManageRoutes {
			return nil, fmt.Errorf("discover physical path: %w", pathErr)
		}
		logger.Warn("物理出口发现失败，当前运行时只能进行未验证的普通直连测试", "component", "network", "error", pathErr)
	}
	directDial, err := cfnetwork.NewBoundDialer(physicalPath.Interface, cfg.Benchmark.ConnectTimeout.Duration())
	if err != nil {
		return nil, fmt.Errorf("create physical interface dialer: %w", err)
	}
	routeBackend := cfnetwork.NewPlatformRouteBackend(cfg.Network.CommandTimeout.Duration())
	// 控制器始终负责崩溃恢复；是否允许新修改仍由当前 Runner 的受控配置决定。
	routeController, err := cfnetwork.NewRouteController(cfg.DataDir, routeBackend, true, logger)
	if err != nil {
		return nil, err
	}
	proxyCoordinator, err := buildProxyCoordinator(cfg, physicalPath, routeController, logger)
	if err != nil {
		return nil, err
	}
	rangeCatalog := ranges.NewCatalog(cfg.Ranges, cfg.DataDir)
	benchmarker := benchmark.New(cfg.Benchmark, directDial)
	runner, err := optimizer.NewRunner(cfg, rangeCatalog, benchmarker, stateStore, routeController, physicalPath, proxyCoordinator, logger)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		Config: cfg, ConfigPath: configPath, Store: stateStore, Ranges: rangeCatalog, Runner: runner,
		Routes: routeController, RouteBackend: routeBackend, PhysicalPath: physicalPath,
		DirectDial: directDial, ProxyCoordinator: proxyCoordinator, Logger: logger,
	}, nil
}

// View 返回同一时刻的运行配置与 Runner，避免持续维护切换期间出现数据竞争。
func (r *Runtime) View() RuntimeView {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return RuntimeView{
		Config: r.Config, Runner: r.Runner, PhysicalPath: r.PhysicalPath,
		DirectDial: r.DirectDial, ProxyCoordinator: r.ProxyCoordinator,
	}
}

// DetectManagedAdapters 只读检测快速流程可用的路由和代理适配器。
func (r *Runtime) DetectManagedAdapters(ctx context.Context, physicalPath cfnetwork.PhysicalPath) (map[string]proxy.Detection, error) {
	view := r.View()
	managedConfig := view.Config
	managedConfig.Network.Interface = physicalPath.Interface
	managedConfig.Network.GatewayIPv4 = physicalPath.GatewayIPv4
	managedConfig.Network.GatewayIPv6 = physicalPath.GatewayIPv6
	managedConfig.Network.ManageRoutes = true
	managedConfig.Proxy.Generic.Enabled = true
	if err := validateManagedPath(managedConfig, physicalPath); err != nil {
		return nil, err
	}
	if err := managedConfig.Validate(); err != nil {
		return nil, fmt.Errorf("validate managed runtime config: %w", err)
	}
	coordinator, err := buildProxyCoordinator(managedConfig, physicalPath, r.Routes, r.Logger)
	if err != nil {
		return nil, err
	}
	if coordinator == nil {
		return detectAutoProxyAdapters(ctx, managedConfig, map[string]proxy.Detection{}), nil
	}
	return detectAutoProxyAdapters(ctx, managedConfig, coordinator.Detect(ctx)), nil
}

// DetectProxyAdapters 只读检测当前运行时及自动发现的本机代理内核。
func (r *Runtime) DetectProxyAdapters(ctx context.Context) map[string]proxy.Detection {
	view := r.View()
	detections := map[string]proxy.Detection{}
	if view.ProxyCoordinator != nil {
		detections = view.ProxyCoordinator.Detect(ctx)
	}
	return detectAutoProxyAdapters(ctx, view.Config, detections)
}

// detectAutoProxyAdapters 在不改变代理配置的前提下补充 Mihomo 自动发现证据。
func detectAutoProxyAdapters(ctx context.Context, cfg config.Config, detections map[string]proxy.Detection) map[string]proxy.Detection {
	if !cfg.Proxy.AutoDetect || detections[cleanupAdapterMihomo].Present {
		return detections
	}
	detection, err := mihomo.AutoDetect(ctx, cfg.Proxy.Mihomo)
	if err != nil && detection.Message == "" {
		detection.Message = err.Error()
	}
	detections[cleanupAdapterMihomo] = detection
	return detections
}

// BuildManagedSession 使用已确认的物理出口和已检测适配器构建可应用系统策略的会话。
func (r *Runtime) BuildManagedSession(physicalPath cfnetwork.PhysicalPath, detections map[string]proxy.Detection) (RuntimeSession, error) {
	view := r.View()
	persistedConfig := view.Config
	persistedConfig.Network.Interface = physicalPath.Interface
	persistedConfig.Network.GatewayIPv4 = physicalPath.GatewayIPv4
	persistedConfig.Network.GatewayIPv6 = physicalPath.GatewayIPv6
	persistedConfig.Network.ManageRoutes = true
	persistedConfig.Proxy.Generic.Enabled = true
	if err := validateManagedPath(persistedConfig, physicalPath); err != nil {
		return RuntimeSession{}, err
	}
	if err := persistedConfig.Validate(); err != nil {
		return RuntimeSession{}, fmt.Errorf("validate managed runtime config: %w", err)
	}
	managedConfig := configForDetectedAdapters(persistedConfig, detections)
	directDial, err := cfnetwork.NewBoundDialer(physicalPath.Interface, managedConfig.Benchmark.ConnectTimeout.Duration())
	if err != nil {
		return RuntimeSession{}, fmt.Errorf("create confirmed physical interface dialer: %w", err)
	}
	proxyCoordinator, err := buildProxyCoordinator(managedConfig, physicalPath, r.Routes, r.Logger)
	if err != nil {
		return RuntimeSession{}, err
	}
	benchmarker := benchmark.New(managedConfig.Benchmark, directDial)
	runner, err := optimizer.NewRunner(managedConfig, r.Ranges, benchmarker, r.Store, r.Routes, physicalPath, proxyCoordinator, r.Logger)
	if err != nil {
		return RuntimeSession{}, err
	}
	return RuntimeSession{
		Config: persistedConfig, Runner: runner, PhysicalPath: physicalPath,
		DirectDial: directDial, ProxyCoordinator: proxyCoordinator,
	}, nil
}

// ActivateSession 在策略验证且配置持久化成功后原子切换调度器使用的运行会话。
func (r *Runtime) ActivateSession(session RuntimeSession) {
	r.mutex.Lock()
	r.Config = session.Config
	r.Runner = session.Runner
	r.PhysicalPath = session.PhysicalPath
	r.DirectDial = session.DirectDial
	r.ProxyCoordinator = session.ProxyCoordinator
	r.mutex.Unlock()
}

// validateManagedPath 要求至少一个已启用地址族具备可验证的物理网关。
func validateManagedPath(cfg config.Config, physicalPath cfnetwork.PhysicalPath) error {
	if physicalPath.Interface == "" {
		return fmt.Errorf("confirmed physical interface is required")
	}
	if cfg.Benchmark.IPv4 && physicalPath.GatewayIPv4 == "" && (!cfg.Benchmark.IPv6 || physicalPath.GatewayIPv6 == "") {
		return fmt.Errorf("confirmed physical path has no gateway for an enabled IP family")
	}
	if cfg.Benchmark.IPv6 && physicalPath.GatewayIPv6 == "" && (!cfg.Benchmark.IPv4 || physicalPath.GatewayIPv4 == "") {
		return fmt.Errorf("confirmed physical path has no gateway for an enabled IP family")
	}
	return nil
}

// configForDetectedAdapters 只让本次预检实际可用的适配器参与策略能力计算。
func configForDetectedAdapters(cfg config.Config, detections map[string]proxy.Detection) config.Config {
	isPresent := func(name string) bool { return detections[name].Present }
	cfg.Proxy.Generic.Enabled = isPresent(cleanupAdapterGeneric)
	cfg.Proxy.Mihomo.Enabled = cfg.Proxy.Mihomo.Enabled && isPresent(cleanupAdapterMihomo)
	if cfg.Proxy.Mihomo.Enabled && detections[cleanupAdapterMihomo].Endpoint != "" {
		cfg.Proxy.Mihomo.Controller = detections[cleanupAdapterMihomo].Endpoint
	}
	cfg.Proxy.SingBox.Enabled = cfg.Proxy.SingBox.Enabled && isPresent(cleanupAdapterSingBox)
	cfg.Proxy.Xray.Enabled = cfg.Proxy.Xray.Enabled && isPresent(cleanupAdapterXray)
	cfg.Proxy.External.Enabled = cfg.Proxy.External.Enabled && isPresent(cleanupAdapterExternal)
	cfg.Hosts.Enabled = cfg.Hosts.Enabled && isPresent(cleanupAdapterHosts)
	return cfg
}

// BuildCleanup 创建不依赖当前物理出口的最小运行时，仅用于卸载前恢复持久化策略。
func BuildCleanup(cfg config.Config, configPath string, logger *slog.Logger) (*Runtime, error) {
	if logger == nil {
		return nil, fmt.Errorf("application logger is required")
	}
	stateStore, err := store.Open(cfg.DataDir, cfg.History.MaxRuns)
	if err != nil {
		return nil, err
	}
	routeBackend := cfnetwork.NewPlatformRouteBackend(cfg.Network.CommandTimeout.Duration())
	routeController, err := cfnetwork.NewRouteController(cfg.DataDir, routeBackend, true, logger)
	if err != nil {
		return nil, err
	}
	cleanupConfig, err := configForStoredReceipts(cfg, stateStore.Snapshot())
	if err != nil {
		return nil, err
	}
	proxyCoordinator, err := buildProxyCoordinator(cleanupConfig, cfnetwork.PhysicalPath{}, routeController, logger)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		Config: cfg, ConfigPath: configPath, Store: stateStore, Routes: routeController,
		RouteBackend: routeBackend, ProxyCoordinator: proxyCoordinator, Logger: logger,
	}, nil
}

// CleanupManagedPolicy 执行卸载专用运行时的路由恢复和累计收据回滚。
func (r *Runtime) CleanupManagedPolicy(ctx context.Context) error {
	return optimizer.CleanupManagedPolicy(ctx, r.Store, r.Routes, r.ProxyCoordinator)
}

func configForStoredReceipts(cfg config.Config, state store.State) (config.Config, error) {
	cfg.Network.ManageRoutes = true
	cfg.Proxy.Generic.Enabled = false
	cfg.Proxy.Mihomo.Enabled = false
	cfg.Proxy.SingBox.Enabled = false
	cfg.Proxy.Xray.Enabled = false
	cfg.Proxy.External.Enabled = false
	cfg.Hosts.Enabled = false
	if state.Policy == nil {
		return cfg, nil
	}
	var applied proxy.ApplyResult
	if err := json.Unmarshal(state.Policy.Receipts, &applied); err != nil {
		return config.Config{}, fmt.Errorf("decode stored policy receipts: %w", err)
	}
	for _, receipt := range applied.Receipts {
		switch receipt.Adapter {
		case cleanupAdapterGeneric:
			cfg.Proxy.Generic.Enabled = true
		case cleanupAdapterMihomo:
			cfg.Proxy.Mihomo.Enabled = true
		case cleanupAdapterSingBox:
			cfg.Proxy.SingBox.Enabled = true
		case cleanupAdapterXray:
			cfg.Proxy.Xray.Enabled = true
		case cleanupAdapterExternal:
			cfg.Proxy.External.Enabled = true
		case cleanupAdapterHosts:
			cfg.Hosts.Enabled = true
		default:
			return config.Config{}, fmt.Errorf("stored policy references unsupported adapter %q", receipt.Adapter)
		}
	}
	return cfg, nil
}

func buildProxyCoordinator(cfg config.Config, physicalPath cfnetwork.PhysicalPath, routeController *cfnetwork.RouteController, logger *slog.Logger) (*proxy.Coordinator, error) {
	var adapters []proxy.Adapter
	if cfg.Proxy.Generic.Enabled && cfg.Network.ManageRoutes {
		adapter, err := generic.New(routeController, physicalPath, 5)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, adapter)
	}
	if cfg.Proxy.Mihomo.Enabled {
		adapter, err := mihomo.New(cfg.Proxy.Mihomo)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, adapter)
	}
	if cfg.Proxy.SingBox.Enabled {
		adapter, err := singbox.New(cfg.Proxy.SingBox)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, adapter)
	}
	if cfg.Proxy.Xray.Enabled {
		adapter, err := xray.New(cfg.Proxy.Xray)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, adapter)
	}
	if cfg.Proxy.External.Enabled {
		adapter, err := external.New(cfg.Proxy.External)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, adapter)
	}
	if cfg.Hosts.Enabled {
		adapter, err := hosts.New(cfg.Hosts)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, adapter)
	}
	if len(adapters) == 0 {
		return nil, nil
	}
	return proxy.NewCoordinator(adapters, logger)
}
