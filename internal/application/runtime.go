package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
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
	DomainResolver   *acceleration.PhysicalResolver
	DomainVerifier   *acceleration.Verifier
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
	if cfg.Network.ManageRoutes {
		managedConfig.Hosts.Enabled = cfg.Acceleration.Enabled
		if cfg.Proxy.AutoDetect {
			detection, detectErr := mihomo.AutoDetect(context.Background(), cfg.Proxy.Mihomo)
			if detectErr == nil && detection.Present {
				if detectedConfig, configureErr := mihomo.ConfigureDetected(cfg.Proxy.Mihomo, detection, cfg.DataDir); configureErr == nil {
					managedConfig.Proxy.Mihomo = detectedConfig
				} else {
					logger.Warn("已发现 Mihomo，但无法建立安全管理路径", "component", "proxy", "adapter", "mihomo", "error", configureErr)
				}
			}
		}
	}
	proxyCoordinator, err := buildProxyCoordinator(managedConfig, physicalPath, routeController, logger)
	if err != nil {
		return nil, err
	}
	rangeCatalog := ranges.NewCatalog(cfg.Ranges, cfg.DataDir)
	benchmarker := benchmark.New(cfg.Benchmark, directDial)
	runner, err := optimizer.NewRunner(managedConfig, rangeCatalog, benchmarker, stateStore, routeController, physicalPath, proxyCoordinator, logger)
	if err != nil {
		return nil, err
	}
	domainVerifier, err := acceleration.NewVerifier(directDial, cfg.Benchmark.TLSTimeout.Duration())
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
		Config: cfg, ConfigPath: configPath, Store: stateStore, Ranges: rangeCatalog, Runner: runner,
		Routes: routeController, RouteBackend: routeBackend, PhysicalPath: physicalPath,
		DirectDial: directDial, ProxyCoordinator: proxyCoordinator, Logger: logger,
		DomainResolver: domainResolver, DomainVerifier: domainVerifier,
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
	managedConfig.Hosts.Enabled = managedConfig.Acceleration.Enabled
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

// DomainDiscoveryResult 汇总一次只读连接观察及可能触发的策略刷新。
type DomainDiscoveryResult struct {
	Observed        int                        `json:"observed"`
	Verified        int                        `json:"verified"`
	Activated       int                        `json:"activated"`
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
	r.mutex.RLock()
	resolver := r.DomainResolver
	verifier := r.DomainVerifier
	runner := r.Runner
	r.mutex.RUnlock()
	if resolver == nil || verifier == nil {
		return r.domainDiscoverySnapshot(), errors.New("domain discovery verification is unavailable")
	}
	rangeSnapshot, err := r.Ranges.Load()
	if err != nil {
		return r.domainDiscoverySnapshot(), err
	}
	stateBefore := r.Store.Snapshot()
	targets := currentSelectionAddresses(stateBefore)
	excluded := make(map[string]struct{}, len(view.Config.Acceleration.ExcludedDomains))
	for _, domain := range view.Config.Acceleration.ExcludedDomains {
		excluded[strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")] = struct{}{}
	}
	now := time.Now().UTC()
	updates := map[string]store.DomainDiscovery{}
	activatedDomains := map[string]struct{}{}
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
		if exists && record.CloudflareVerified && record.PreflightVerified {
			record.LastSeenAt = now
			if view.Config.Acceleration.AutoApply && !record.Active {
				record.Active = true
				record.LastError = ""
				activatedDomains[domain] = struct{}{}
			}
			updates[domain] = record
			result.Verified++
			continue
		}
		resolved, resolveErr := resolver.Resolve(ctx, domain)
		if resolveErr != nil || !allCloudflareAddresses(rangeSnapshot, resolved) {
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
		record.CloudflareVerified = true
		record.LastResolvedAddresses = resolvedStrings
		record.LastError = ""
		if len(targets) == 0 {
			record.PreflightVerified = false
			record.Active = false
			record.LastError = "尚无已验证优选 IP"
		} else if verifyErr := verifier.VerifyPreflight(ctx, []proxy.DomainMapping{{Domain: domain, Addresses: targets}}); verifyErr != nil {
			record.PreflightVerified = false
			record.Active = false
			record.LastError = verifyErr.Error()
		} else {
			record.PreflightVerified = true
			record.Active = view.Config.Acceleration.AutoApply
			if record.Active {
				activatedDomains[domain] = struct{}{}
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
	if result.Activated > 0 && view.Config.Acceleration.AutoApply {
		if runner == nil {
			return r.domainDiscoverySnapshot(), errors.New("optimizer runner is unavailable for automatic domain activation")
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
	result.Domains = snapshot.Domains
	return result, nil
}

// domainDiscoverySnapshot 合并持久化发现记录、手动域名和当前生效映射，供 IPC 只读展示。
func (r *Runtime) domainDiscoverySnapshot() DomainDiscoveryResult {
	state := r.Store.Snapshot()
	view := r.View()
	records := make(map[string]store.DomainDiscovery, len(state.DiscoveredDomains)+len(view.Config.Acceleration.ManualDomains))
	for domain, record := range state.DiscoveredDomains {
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
	result := DomainDiscoveryResult{Domains: make([]DomainAccelerationStatus, 0, len(records))}
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

// currentSelectionAddresses 返回当前已经完成策略验证的 IPv4/IPv6 优选地址。
func currentSelectionAddresses(state store.State) []string {
	var result []string
	for _, selection := range []*store.Selection{state.CurrentIPv4, state.CurrentIPv6} {
		if selection == nil || !selection.PolicyVerified {
			continue
		}
		if address, err := netip.ParseAddr(selection.IP); err == nil {
			result = append(result, address.Unmap().String())
		}
	}
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
	proxyCoordinator, err := buildProxyCoordinator(managedConfig, physicalPath, r.Routes, r.Logger)
	if err != nil {
		return RuntimeSession{}, err
	}
	benchmarker := benchmark.New(managedConfig.Benchmark, directDial)
	runner, err := optimizer.NewRunner(managedConfig, r.Ranges, benchmarker, r.Store, r.Routes, physicalPath, proxyCoordinator, r.Logger)
	if err != nil {
		return RuntimeSession{}, err
	}
	domainVerifier, err := acceleration.NewVerifier(directDial, managedConfig.Benchmark.TLSTimeout.Duration())
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
