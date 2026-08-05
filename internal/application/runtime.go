package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/acceleration"
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

// ErrRuntimeRestartRequired 表示配置触及无法在现有 IPC/状态目录上热切换的进程边界。
var ErrRuntimeRestartRequired = errors.New("runtime reload requires service restart")

// Runtime 汇总一个进程共享的核心组件和已验证依赖关系。
type Runtime struct {
	mutex             sync.RWMutex
	accelerationMutex sync.Mutex
	Config            config.Config
	ConfigPath        string
	Store             *store.Store
	Ranges            *ranges.Catalog
	Runner            *optimizer.Runner
	Routes            *cfnetwork.RouteController
	RouteBackend      cfnetwork.RouteBackend
	PhysicalPath      cfnetwork.PhysicalPath
	DirectDial        cfnetwork.DialContextFunc
	ProxyCoordinator  *proxy.Coordinator
	DomainResolver    *acceleration.PhysicalResolver
	DomainVerifier    *acceleration.Verifier
	Logger            *slog.Logger
	configChanged     chan struct{}
	// mihomoAutoDetected 标记当前运行时是否使用了自动探测到的 Mihomo 端点。
	mihomoAutoDetected bool
}

// RuntimeView 提供可并发读取的当前运行配置和执行依赖快照。
type RuntimeView struct {
	Config             config.Config
	Ranges             *ranges.Catalog
	Runner             *optimizer.Runner
	Routes             *cfnetwork.RouteController
	RouteBackend       cfnetwork.RouteBackend
	PhysicalPath       cfnetwork.PhysicalPath
	DirectDial         cfnetwork.DialContextFunc
	ProxyCoordinator   *proxy.Coordinator
	DomainResolver     *acceleration.PhysicalResolver
	DomainVerifier     *acceleration.Verifier
	MihomoAutoDetected bool
}

// RuntimeSession 描述一次快速流程使用且可在验证后激活的完整运行会话。
type RuntimeSession struct {
	Config           config.Config
	Runner           *optimizer.Runner
	PhysicalPath     cfnetwork.PhysicalPath
	DirectDial       cfnetwork.DialContextFunc
	ProxyCoordinator *proxy.Coordinator
	DomainResolver   *acceleration.PhysicalResolver
	DomainVerifier   *acceleration.Verifier
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
	managedConfig := cfg
	runtimeConfig := cfg
	mihomoAutoDetected := false
	if cfg.Network.ManageRoutes {
		managedConfig.Hosts.Enabled = cfg.Acceleration.Enabled
		if cfg.Proxy.AutoDetect {
			detection, detectErr := mihomo.AutoDetect(context.Background(), cfg.Proxy.Mihomo)
			if detectErr == nil && detection.Present {
				if detectedConfig, configureErr := mihomo.ConfigureDetected(cfg.Proxy.Mihomo, detection, cfg.DataDir); configureErr == nil {
					managedConfig.Proxy.Mihomo = detectedConfig
					runtimeConfig.Proxy.Mihomo = detectedConfig
					mihomoAutoDetected = true
				} else {
					logger.Warn("已发现 Mihomo，但无法建立安全管理路径", "component", "proxy", "adapter", "mihomo", "error", configureErr)
				}
			}
		}
	}
	proxyCoordinator, err := buildProxyCoordinator(managedConfig, physicalPath, routeController, directDial, logger)
	if err != nil {
		return nil, err
	}
	rangeCatalog := ranges.NewCatalog(cfg.Ranges, cfg.DataDir)
	benchmarker := benchmark.New(cfg.Benchmark, directDial)
	runner, err := optimizer.NewRunner(managedConfig, rangeCatalog, benchmarker, stateStore, routeController, physicalPath, proxyCoordinator, logger)
	if err != nil {
		return nil, err
	}
	domainVerifier, err := acceleration.NewVerifierWithOptions(directDial, acceleration.VerificationOptions{
		PreflightTimeout: managedConfig.Benchmark.TLSTimeout.Duration(),
		ApplyTimeout:     managedConfig.Acceleration.ApplyVerificationTimeout.Duration(),
		AttemptTimeout:   managedConfig.Acceleration.ApplyAttemptTimeout.Duration(),
		RetryInterval:    managedConfig.Acceleration.ApplyRetryInterval.Duration(),
		MaxAttempts:      managedConfig.Acceleration.ApplyMaxAttempts,
	})
	if err != nil {
		return nil, err
	}
	runner.SetDomainMappingVerifier(domainVerifier)
	dnsServers, err := cfnetwork.DiscoverPhysicalDNSServers(context.Background(), physicalPath, cfg.Network.CommandTimeout.Duration())
	if err != nil {
		return nil, fmt.Errorf("discover physical DNS servers: %w", err)
	}
	domainResolver, err := acceleration.NewPhysicalResolver(directDial, dnsServers, cfg.Ranges.RequestTimeout.Duration())
	if err != nil {
		return nil, err
	}
	runner.SetDomainResolver(domainResolver)
	return &Runtime{
		Config: runtimeConfig, ConfigPath: configPath, Store: stateStore, Ranges: rangeCatalog, Runner: runner,
		Routes: routeController, RouteBackend: routeBackend, PhysicalPath: physicalPath,
		DirectDial: directDial, ProxyCoordinator: proxyCoordinator, Logger: logger, mihomoAutoDetected: mihomoAutoDetected,
		DomainResolver: domainResolver, DomainVerifier: domainVerifier,
		configChanged: make(chan struct{}),
	}, nil
}

// View 返回同一时刻的运行配置与 Runner，避免持续维护切换期间出现数据竞争。
func (r *Runtime) View() RuntimeView {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return RuntimeView{
		Config: r.Config, Ranges: r.Ranges, Runner: r.Runner, Routes: r.Routes, RouteBackend: r.RouteBackend,
		PhysicalPath: r.PhysicalPath, DirectDial: r.DirectDial, ProxyCoordinator: r.ProxyCoordinator,
		DomainResolver: r.DomainResolver, DomainVerifier: r.DomainVerifier, MihomoAutoDetected: r.mihomoAutoDetected,
	}
}

// ConfigChanges 返回下一次运行配置切换时关闭的广播通道。
func (r *Runtime) ConfigChanges() <-chan struct{} {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if r.configChanged == nil {
		r.configChanged = make(chan struct{})
	}
	return r.configChanged
}

// ReloadConfig 在无活动任务时重建运行依赖并热切换完整配置。
func (r *Runtime) ReloadConfig(ctx context.Context, cfg config.Config, refreshPolicy bool) (bool, error) {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return false, err
	}
	if !r.accelerationMutex.TryLock() {
		return false, optimizer.ErrAlreadyRunning
	}
	defer r.accelerationMutex.Unlock()
	view := r.View()
	if view.Runner == nil {
		return false, errors.New("optimizer runner is unavailable for runtime reload")
	}
	if cfg.DataDir != view.Config.DataDir || cfg.IPC.Endpoint != view.Config.IPC.Endpoint {
		return false, ErrRuntimeRestartRequired
	}
	physicalPath, pathErr := cfnetwork.DiscoverPhysicalPath(
		ctx, cfg.Network.Interface, cfg.Network.GatewayIPv4, cfg.Network.GatewayIPv6, cfg.Network.CommandTimeout.Duration(),
	)
	if pathErr != nil {
		if cfg.Network.ManageRoutes {
			return false, fmt.Errorf("discover physical path for runtime reload: %w", pathErr)
		}
		physicalPath = view.PhysicalPath
		r.Logger.Warn("热重载时物理出口发现失败，继续使用上一条只读路径", "component", "network", "error", pathErr)
	}
	directDial, err := cfnetwork.NewBoundDialer(physicalPath.Interface, cfg.Benchmark.ConnectTimeout.Duration())
	if err != nil {
		return false, fmt.Errorf("create reloaded physical interface dialer: %w", err)
	}
	routeBackend := cfnetwork.NewPlatformRouteBackend(cfg.Network.CommandTimeout.Duration())
	routeController, err := cfnetwork.NewRouteController(cfg.DataDir, routeBackend, true, r.Logger)
	if err != nil {
		return false, fmt.Errorf("create reloaded route controller: %w", err)
	}
	managedConfig := cfg
	runtimeConfig := cfg
	mihomoAutoDetected := false
	if cfg.Network.ManageRoutes {
		managedConfig.Hosts.Enabled = cfg.Acceleration.Enabled
		if cfg.Proxy.AutoDetect {
			detection, detectErr := mihomo.AutoDetect(ctx, cfg.Proxy.Mihomo)
			if detectErr == nil && detection.Present {
				if detectedConfig, configureErr := mihomo.ConfigureDetected(cfg.Proxy.Mihomo, detection, cfg.DataDir); configureErr == nil {
					managedConfig.Proxy.Mihomo = detectedConfig
					runtimeConfig.Proxy.Mihomo = detectedConfig
					mihomoAutoDetected = true
				} else {
					r.Logger.Warn("热重载已发现 Mihomo，但无法建立安全管理路径", "component", "proxy", "adapter", "mihomo", "error", configureErr)
				}
			}
		}
	}
	proxyCoordinator, err := buildProxyCoordinator(managedConfig, physicalPath, routeController, directDial, r.Logger)
	if err != nil {
		return false, err
	}
	rangeCatalog := ranges.NewCatalog(cfg.Ranges, cfg.DataDir)
	benchmarker := benchmark.New(managedConfig.Benchmark, directDial)
	domainVerifier, err := acceleration.NewVerifierWithOptions(directDial, acceleration.VerificationOptions{
		PreflightTimeout: managedConfig.Benchmark.TLSTimeout.Duration(),
		ApplyTimeout:     managedConfig.Acceleration.ApplyVerificationTimeout.Duration(),
		AttemptTimeout:   managedConfig.Acceleration.ApplyAttemptTimeout.Duration(),
		RetryInterval:    managedConfig.Acceleration.ApplyRetryInterval.Duration(),
		MaxAttempts:      managedConfig.Acceleration.ApplyMaxAttempts,
	})
	if err != nil {
		return false, err
	}
	dnsServers, err := cfnetwork.DiscoverPhysicalDNSServers(ctx, physicalPath, cfg.Network.CommandTimeout.Duration())
	if err != nil {
		return false, fmt.Errorf("discover physical DNS servers for runtime reload: %w", err)
	}
	domainResolver, err := acceleration.NewPhysicalResolver(directDial, dnsServers, cfg.Ranges.RequestTimeout.Duration())
	if err != nil {
		return false, err
	}
	policyRefreshed, err := view.Runner.Reconfigure(
		ctx, managedConfig, rangeCatalog, benchmarker, routeController, physicalPath,
		proxyCoordinator, domainResolver, domainVerifier, refreshPolicy,
	)
	if err != nil {
		return false, err
	}
	r.mutex.Lock()
	r.Config = runtimeConfig
	r.Ranges = rangeCatalog
	r.Routes = routeController
	r.RouteBackend = routeBackend
	r.PhysicalPath = physicalPath
	r.DirectDial = directDial
	r.ProxyCoordinator = proxyCoordinator
	r.DomainResolver = domainResolver
	r.DomainVerifier = domainVerifier
	r.mihomoAutoDetected = mihomoAutoDetected
	r.notifyConfigChangedLocked()
	r.mutex.Unlock()
	r.Store.SetMaxRuns(cfg.History.MaxRuns)
	r.Logger.Info("后台运行配置热重载完成", "component", "config", "policy_refreshed", policyRefreshed, "result", "completed")
	return policyRefreshed, nil
}

// notifyConfigChangedLocked 在已持有运行时写锁时广播新的配置版本。
func (r *Runtime) notifyConfigChangedLocked() {
	if r.configChanged == nil {
		r.configChanged = make(chan struct{})
		return
	}
	close(r.configChanged)
	r.configChanged = make(chan struct{})
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
	managedConfig.Hosts.Enabled = managedConfig.Acceleration.Enabled
	if err := validateManagedPath(managedConfig, physicalPath); err != nil {
		return nil, err
	}
	if err := managedConfig.Validate(); err != nil {
		return nil, fmt.Errorf("validate managed runtime config: %w", err)
	}
	coordinator, err := buildProxyCoordinator(managedConfig, physicalPath, view.Routes, nil, r.Logger)
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

// DomainDiscoveryResult 汇总一次只读连接观察及可能触发的策略刷新。
type DomainDiscoveryResult struct {
	Observed        int                        `json:"observed"`
	Verified        int                        `json:"verified"`
	Activated       int                        `json:"activated"`
	Discovered      int                        `json:"discovered"`
	PolicyRefreshed bool                       `json:"policy_refreshed"`
	Domains         []DomainAccelerationStatus `json:"domains"`
}

// DomainAccelerationStatus 提供域名发现状态和不含回滚正文的已验证策略证据。
type DomainAccelerationStatus struct {
	store.DomainDiscovery
	AcceleratedAddresses []string  `json:"accelerated_addresses,omitempty"`
	VerifiedAdapters     []string  `json:"verified_adapters,omitempty"`
	AppliedAt            time.Time `json:"applied_at,omitempty"`
}

// DiscoverAccelerationDomains 从 Mihomo 活动连接发现精确域名，并用物理 DNS 与 HTTPS 预检确认。
func (r *Runtime) DiscoverAccelerationDomains(ctx context.Context) (DomainDiscoveryResult, error) {
	if !r.accelerationMutex.TryLock() {
		return r.domainDiscoverySnapshot(), errors.New("domain discovery or cleanup is already active")
	}
	defer r.accelerationMutex.Unlock()
	view := r.View()
	if !view.Config.Acceleration.Enabled || !view.Config.Acceleration.AutoDiscover || !view.Config.Proxy.AutoDetect {
		return r.domainDiscoverySnapshot(), nil
	}
	detection, err := mihomo.AutoDetect(ctx, view.Config.Proxy.Mihomo)
	if err != nil || !detection.Present {
		if err != nil {
			return r.domainDiscoverySnapshot(), err
		}
		return r.domainDiscoverySnapshot(), nil
	}
	mihomoConfig := view.Config.Proxy.Mihomo
	mihomoConfig.Controller = detection.Endpoint
	adapter, err := mihomo.New(mihomoConfig)
	if err != nil {
		return r.domainDiscoverySnapshot(), err
	}
	observations, err := adapter.ObserveConnections(ctx)
	if err != nil {
		return r.domainDiscoverySnapshot(), err
	}
	resolver := view.DomainResolver
	verifier := view.DomainVerifier
	runner := view.Runner
	if resolver == nil || verifier == nil {
		return r.domainDiscoverySnapshot(), errors.New("domain discovery verification is unavailable")
	}
	rangeSnapshot, err := view.Ranges.Load()
	if err != nil {
		return r.domainDiscoverySnapshot(), err
	}
	stateBefore := r.Store.Snapshot()
	excluded := make(map[string]struct{}, len(view.Config.Acceleration.ExcludedDomains))
	for _, domain := range view.Config.Acceleration.ExcludedDomains {
		excluded[strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")] = struct{}{}
	}
	now := time.Now().UTC()
	updates := map[string]store.DomainDiscovery{}
	activatedDomains := map[string]struct{}{}
	automaticAllocationEnabled := view.Config.Acceleration.Enabled && view.Config.Acceleration.AutoDiscover && view.Config.Acceleration.AutoApply
	policyChanged := domainPolicyNeedsRefresh(view.Config, stateBefore)
	for domain, record := range stateBefore.DiscoveredDomains {
		_, blocked := excluded[domain]
		if !record.Active || (automaticAllocationEnabled && !blocked) {
			continue
		}
		record.Active = false
		record.LastError = "自动应用已关闭或域名已排除"
		updates[domain] = record
		policyChanged = true
	}
	result := DomainDiscoveryResult{Observed: len(observations)}
	for _, observation := range observations {
		normalized, normalizeErr := (proxy.DirectPolicy{Domains: []string{observation.Host}}).Normalize()
		if normalizeErr != nil || len(normalized.Domains) != 1 {
			continue
		}
		domain := normalized.Domains[0]
		if _, blocked := excluded[domain]; blocked {
			continue
		}
		record, exists := stateBefore.DiscoveredDomains[domain]
		wasActive := exists && record.Active
		resolved, resolveErr := resolver.Resolve(ctx, domain)
		if resolveErr != nil {
			if exists {
				record.LastSeenAt = now
				record.LastError = resolveErr.Error()
				updates[domain] = record
			}
			continue
		}
		resolvedStrings := make([]string, 0, len(resolved))
		for _, address := range resolved {
			resolvedStrings = append(resolvedStrings, address.String())
		}
		if !exists {
			record = store.DomainDiscovery{Domain: domain, Source: "mihomo", FirstSeenAt: now}
		}
		record.LastSeenAt = now
		if !allCloudflareAddresses(rangeSnapshot, resolved) {
			record.CloudflareVerified = false
			record.PreflightVerified = false
			record.Active = false
			record.LastResolvedAddresses = resolvedStrings
			record.LastError = "物理 DNS 地址不完全属于已验证 Cloudflare 网段"
			updates[domain] = record
			policyChanged = policyChanged || wasActive
			continue
		}
		if record.CloudflareVerified && record.PreflightVerified && slices.Equal(record.LastResolvedAddresses, resolvedStrings) {
			record.Active = automaticAllocationEnabled
			record.LastError = ""
			updates[domain] = record
			if record.Active && !wasActive {
				activatedDomains[domain] = struct{}{}
				policyChanged = true
			}
			result.Verified++
			continue
		}
		record.CloudflareVerified = true
		record.LastResolvedAddresses = resolvedStrings
		record.LastError = ""
		if verifyErr := verifier.VerifyPreflight(ctx, []proxy.DomainMapping{{Domain: domain, Addresses: resolvedStrings}}); verifyErr != nil {
			record.PreflightVerified = false
			record.Active = false
			record.LastError = verifyErr.Error()
			policyChanged = policyChanged || wasActive
		} else {
			record.PreflightVerified = true
			record.Active = automaticAllocationEnabled
			if record.Active {
				activatedDomains[domain] = struct{}{}
				policyChanged = true
			}
			result.Verified++
		}
		updates[domain] = record
	}
	if len(updates) > 0 {
		if err := r.Store.Update(func(state *store.State) error {
			for domain, record := range updates {
				state.DiscoveredDomains[domain] = record
			}
			trimDiscoveredDomains(state.DiscoveredDomains, view.Config.Acceleration.MaxDiscoveredDomains)
			return nil
		}); err != nil {
			return r.domainDiscoverySnapshot(), err
		}
	}
	result.Activated = len(activatedDomains)
	if policyChanged {
		if runner == nil {
			return r.domainDiscoverySnapshot(), errors.New("optimizer runner is unavailable for automatic domain policy refresh")
		}
		if err := runner.RefreshPolicy(ctx); err != nil {
			_ = r.Store.Update(func(state *store.State) error {
				for domain := range activatedDomains {
					record := state.DiscoveredDomains[domain]
					record.Active = false
					record.LastError = "自动应用失败: " + err.Error()
					state.DiscoveredDomains[domain] = record
				}
				return nil
			})
			return r.domainDiscoverySnapshot(), err
		}
		result.PolicyRefreshed = true
	}
	snapshot := r.domainDiscoverySnapshot()
	result.Discovered = snapshot.Discovered
	result.Domains = snapshot.Domains
	return result, nil
}

// domainPolicyNeedsRefresh 检测需清理的自动映射和违反 IP 独占约束的旧策略。
func domainPolicyNeedsRefresh(cfg config.Config, state store.State) bool {
	if state.Policy == nil {
		return false
	}
	discoveries := acceleration.EffectiveDiscoveries(cfg, state)
	manual := make(map[string]struct{}, len(cfg.AccelerationDomains()))
	for _, domain := range cfg.AccelerationDomains() {
		manual[domain] = struct{}{}
	}
	expected := make(map[string]struct{}, len(discoveries))
	for _, discovery := range discoveries {
		expected[discovery.Domain] = struct{}{}
	}
	actual := make(map[string]struct{}, len(state.Policy.DomainMappings))
	assignedAddresses := make(map[string]string, len(state.Policy.DomainMappings))
	for _, mapping := range state.Policy.DomainMappings {
		if len(mapping.Addresses) != 1 {
			return true
		}
		address := mapping.Addresses[0]
		if owner, duplicate := assignedAddresses[address]; duplicate && owner != mapping.Domain {
			return true
		}
		assignedAddresses[address] = mapping.Domain
		if _, isManual := manual[mapping.Domain]; !isManual {
			actual[mapping.Domain] = struct{}{}
		}
	}
	for domain := range actual {
		if _, exists := expected[domain]; !exists {
			return true
		}
	}
	return false
}

// domainDiscoverySnapshot 合并持久化发现记录、手动域名和当前生效映射，供 IPC 只读展示。
func (r *Runtime) domainDiscoverySnapshot() DomainDiscoveryResult {
	state := r.Store.Snapshot()
	view := r.View()
	records := make(map[string]store.DomainDiscovery, len(state.DiscoveredDomains)+len(view.Config.Acceleration.ManualDomains))
	configuredManualDomains := make(map[string]struct{}, len(view.Config.Acceleration.ManualDomains))
	for _, domain := range view.Config.AccelerationDomains() {
		configuredManualDomains[domain] = struct{}{}
	}
	for domain, record := range state.DiscoveredDomains {
		if record.Source == "manual" {
			if _, configured := configuredManualDomains[domain]; !configured {
				continue
			}
		}
		records[domain] = record
	}
	activeMappings := map[string][]string{}
	var verifiedAdapters []string
	var policyAppliedAt time.Time
	if state.Policy != nil {
		policyAppliedAt = state.Policy.AppliedAt
		for _, mapping := range state.Policy.DomainMappings {
			activeMappings[mapping.Domain] = append([]string(nil), mapping.Addresses...)
		}
		var applied proxy.ApplyResult
		if err := json.Unmarshal(state.Policy.Receipts, &applied); err == nil {
			seenAdapters := map[string]struct{}{}
			for _, receipt := range applied.Receipts {
				if receipt.Adapter != "" {
					seenAdapters[receipt.Adapter] = struct{}{}
				}
			}
			for adapter := range seenAdapters {
				verifiedAdapters = append(verifiedAdapters, adapter)
			}
			sort.Strings(verifiedAdapters)
		}
	}
	for _, domain := range view.Config.AccelerationDomains() {
		record := records[domain]
		record.Domain = domain
		record.Source = "manual"
		if _, active := activeMappings[domain]; active {
			record.CloudflareVerified = true
			record.PreflightVerified = true
			record.Active = true
		}
		records[domain] = record
	}
	result := DomainDiscoveryResult{Discovered: len(state.DiscoveredDomains), Domains: make([]DomainAccelerationStatus, 0, len(records))}
	for _, record := range records {
		status := DomainAccelerationStatus{DomainDiscovery: record}
		if addresses, active := activeMappings[record.Domain]; active {
			status.LastError = ""
			status.AcceleratedAddresses = append([]string(nil), addresses...)
			status.VerifiedAdapters = append([]string(nil), verifiedAdapters...)
			status.AppliedAt = policyAppliedAt
		}
		result.Domains = append(result.Domains, status)
	}
	sort.Slice(result.Domains, func(i, j int) bool {
		if result.Domains[i].Source != result.Domains[j].Source {
			return result.Domains[i].Source == "manual"
		}
		return result.Domains[i].Domain < result.Domains[j].Domain
	})
	return result
}

// allCloudflareAddresses 要求物理 DNS 返回的每个地址都属于已验证 Cloudflare 网段。
func allCloudflareAddresses(snapshot ranges.Snapshot, addresses []netip.Addr) bool {
	if len(addresses) == 0 {
		return false
	}
	for _, address := range addresses {
		if !snapshot.Contains(address) {
			return false
		}
	}
	return true
}

// trimDiscoveredDomains 优先淘汰最旧的非活动记录，不移除已经生效的域名。
func trimDiscoveredDomains(domains map[string]store.DomainDiscovery, maximum int) {
	if len(domains) <= maximum {
		return
	}
	type candidate struct {
		domain   string
		lastSeen time.Time
	}
	var candidates []candidate
	for domain, record := range domains {
		if !record.Active {
			candidates = append(candidates, candidate{domain: domain, lastSeen: record.LastSeenAt})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].lastSeen.Before(candidates[j].lastSeen) })
	for _, item := range candidates {
		if len(domains) <= maximum {
			break
		}
		delete(domains, item.domain)
	}
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
	if detection.Present {
		managed, configureErr := mihomo.ConfigureDetected(cfg.Proxy.Mihomo, detection, cfg.DataDir)
		if configureErr != nil {
			detection.Manageable = false
			detection.Message = strings.TrimSpace(detection.Message + "; 已发现控制 API，但无法定位可安全重载的活动配置: " + configureErr.Error())
		} else {
			detection.Manageable = true
			detection.ConfigPath = managed.ReloadConfig
		}
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
	persistedConfig.Hosts.Enabled = persistedConfig.Acceleration.Enabled
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
	proxyCoordinator, err := buildProxyCoordinator(managedConfig, physicalPath, view.Routes, directDial, r.Logger)
	if err != nil {
		return RuntimeSession{}, err
	}
	benchmarker := benchmark.New(managedConfig.Benchmark, directDial)
	runner, err := optimizer.NewRunner(managedConfig, view.Ranges, benchmarker, r.Store, view.Routes, physicalPath, proxyCoordinator, r.Logger)
	if err != nil {
		return RuntimeSession{}, err
	}
	domainVerifier, err := acceleration.NewVerifierWithOptions(directDial, acceleration.VerificationOptions{
		PreflightTimeout: managedConfig.Benchmark.TLSTimeout.Duration(),
		ApplyTimeout:     managedConfig.Acceleration.ApplyVerificationTimeout.Duration(),
		AttemptTimeout:   managedConfig.Acceleration.ApplyAttemptTimeout.Duration(),
		RetryInterval:    managedConfig.Acceleration.ApplyRetryInterval.Duration(),
		MaxAttempts:      managedConfig.Acceleration.ApplyMaxAttempts,
	})
	if err != nil {
		return RuntimeSession{}, err
	}
	runner.SetDomainMappingVerifier(domainVerifier)
	dnsServers, err := cfnetwork.DiscoverPhysicalDNSServers(context.Background(), physicalPath, managedConfig.Network.CommandTimeout.Duration())
	if err != nil {
		return RuntimeSession{}, fmt.Errorf("discover confirmed physical DNS servers: %w", err)
	}
	domainResolver, err := acceleration.NewPhysicalResolver(directDial, dnsServers, managedConfig.Ranges.RequestTimeout.Duration())
	if err != nil {
		return RuntimeSession{}, err
	}
	runner.SetDomainResolver(domainResolver)
	return RuntimeSession{
		Config: persistedConfig, Runner: runner, PhysicalPath: physicalPath,
		DirectDial: directDial, ProxyCoordinator: proxyCoordinator,
		DomainResolver: domainResolver, DomainVerifier: domainVerifier,
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
	r.DomainResolver = session.DomainResolver
	r.DomainVerifier = session.DomainVerifier
	r.notifyConfigChangedLocked()
	r.mutex.Unlock()
}

// UpdateAccelerationDomains 更新 Runner 的域名配置，并返回活动策略是否已完成刷新。
func (r *Runtime) UpdateAccelerationDomains(ctx context.Context, manualDomains, excludedDomains []string) (bool, error) {
	view := r.View()
	if view.Runner == nil {
		return false, errors.New("optimizer runner is unavailable for acceleration domain update")
	}
	policyRefreshed, err := view.Runner.UpdateAccelerationDomains(ctx, manualDomains, excludedDomains)
	if err != nil {
		return false, err
	}
	r.mutex.Lock()
	r.Config.Acceleration.ManualDomains = slices.Clone(manualDomains)
	r.Config.Acceleration.ExcludedDomains = slices.Clone(excludedDomains)
	r.mutex.Unlock()
	return policyRefreshed, nil
}

// ClearDiscoveredAccelerationDomains 串行清理自动发现记录及其已验证加速策略。
func (r *Runtime) ClearDiscoveredAccelerationDomains(ctx context.Context) (optimizer.DiscoveredDomainCleanupResult, error) {
	if !r.accelerationMutex.TryLock() {
		return optimizer.DiscoveredDomainCleanupResult{}, errors.New("domain discovery or cleanup is already active")
	}
	defer r.accelerationMutex.Unlock()
	view := r.View()
	if view.Runner == nil {
		return optimizer.DiscoveredDomainCleanupResult{}, errors.New("optimizer runner is unavailable for discovered domain cleanup")
	}
	return view.Runner.ClearDiscoveredDomains(ctx)
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
	cfg.Proxy.Mihomo.Enabled = false
	if detection := detections[cleanupAdapterMihomo]; detection.Present && detection.Manageable {
		if managed, err := mihomo.ConfigureDetected(cfg.Proxy.Mihomo, detection, cfg.DataDir); err == nil {
			cfg.Proxy.Mihomo = managed
		}
	}
	cfg.Proxy.SingBox.Enabled = cfg.Proxy.SingBox.Enabled && isPresent(cleanupAdapterSingBox)
	cfg.Proxy.Xray.Enabled = cfg.Proxy.Xray.Enabled && isPresent(cleanupAdapterXray)
	cfg.Proxy.External.Enabled = cfg.Proxy.External.Enabled && isPresent(cleanupAdapterExternal)
	cfg.Hosts.Enabled = cfg.Acceleration.Enabled && isPresent(cleanupAdapterHosts)
	return cfg
}

// mergeDetectedMihomoConfig 将自动探测到的有效控制端合并到界面配置快照，不覆盖用户的其他持久化设置。
func mergeDetectedMihomoConfig(persisted, effective config.Config, autoDetected bool) config.Config {
	if !autoDetected || !effective.Proxy.Mihomo.Enabled || !persisted.Proxy.AutoDetect {
		return persisted
	}
	persisted.Proxy.Mihomo = effective.Proxy.Mihomo
	return persisted
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
	proxyCoordinator, err := buildProxyCoordinator(cleanupConfig, cfnetwork.PhysicalPath{}, routeController, nil, logger)
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
	view := r.View()
	return optimizer.CleanupManagedPolicy(ctx, r.Store, view.Routes, view.ProxyCoordinator)
}

// RecoverPendingPolicy 恢复进程退出前已应用但尚未提交的代理策略事务。
func (r *Runtime) RecoverPendingPolicy(ctx context.Context) error {
	return optimizer.RecoverPendingPolicy(ctx, r.Store, r.View().ProxyCoordinator)
}

func configForStoredReceipts(cfg config.Config, state store.State) (config.Config, error) {
	cfg.Network.ManageRoutes = true
	cfg.Proxy.Generic.Enabled = false
	cfg.Proxy.Mihomo.Enabled = false
	cfg.Proxy.SingBox.Enabled = false
	cfg.Proxy.Xray.Enabled = false
	cfg.Proxy.External.Enabled = false
	cfg.Hosts.Enabled = false
	if state.Policy == nil && state.PendingPolicy == nil {
		return cfg, nil
	}
	var receiptPayloads []json.RawMessage
	if state.Policy != nil {
		receiptPayloads = append(receiptPayloads, state.Policy.Receipts)
	}
	if state.PendingPolicy != nil {
		receiptPayloads = append(receiptPayloads, state.PendingPolicy.Receipts)
	}
	for _, payload := range receiptPayloads {
		var applied proxy.ApplyResult
		if err := json.Unmarshal(payload, &applied); err != nil {
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
	}
	return cfg, nil
}

func buildProxyCoordinator(cfg config.Config, physicalPath cfnetwork.PhysicalPath, routeController *cfnetwork.RouteController, benchmarkDial cfnetwork.DialContextFunc, logger *slog.Logger) (*proxy.Coordinator, error) {
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
		adapter.SetConnectionVerificationWindow(
			cfg.Acceleration.ApplyVerificationTimeout.Duration(),
			cfg.Acceleration.ApplyAttemptTimeout.Duration(),
			cfg.Acceleration.ApplyRetryInterval.Duration(),
			cfg.Acceleration.ApplyMaxAttempts,
		)
		if benchmarkDial != nil {
			adapter.SetBenchmarkDialer(physicalPath.Interface, benchmarkDial)
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
