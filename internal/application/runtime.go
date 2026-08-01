package application

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

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
	routeController, err := cfnetwork.NewRouteController(cfg.DataDir, routeBackend, cfg.Network.ManageRoutes, logger)
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
