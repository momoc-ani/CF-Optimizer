package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/benchmark"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/ipc"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/optimizer"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/ranges"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

type quickStartRouteBackend struct {
	mutex        sync.Mutex
	routes       map[string]cfnetwork.RouteSpec
	replacements int
	deletions    int
	resolveFail  bool
}

func (b *quickStartRouteBackend) Replace(_ context.Context, route cfnetwork.RouteSpec) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.replacements++
	b.routes[route.Prefix] = route
	return nil
}

func (b *quickStartRouteBackend) Delete(_ context.Context, route cfnetwork.RouteSpec) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.deletions++
	if _, exists := b.routes[route.Prefix]; !exists {
		return cfnetwork.ErrRouteNotFound
	}
	delete(b.routes, route.Prefix)
	return nil
}

func (b *quickStartRouteBackend) Get(_ context.Context, prefix string) (cfnetwork.RouteSpec, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	route, exists := b.routes[prefix]
	if !exists {
		return cfnetwork.RouteSpec{}, cfnetwork.ErrRouteNotFound
	}
	return route, nil
}

func (b *quickStartRouteBackend) Resolve(_ context.Context, target netip.Addr) (cfnetwork.ResolvedRoute, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	if b.resolveFail {
		return cfnetwork.ResolvedRoute{}, errors.New("forced route verification failure")
	}
	for _, route := range b.routes {
		if netip.MustParsePrefix(route.Prefix).Contains(target) {
			return cfnetwork.ResolvedRoute{RouteSpec: route, SourceAddress: "192.0.2.20"}, nil
		}
	}
	return cfnetwork.ResolvedRoute{}, cfnetwork.ErrRouteNotFound
}

type quickStartRanges struct{}

func (quickStartRanges) Update(context.Context, bool) (ranges.UpdateResult, error) {
	return ranges.UpdateResult{Snapshot: ranges.Snapshot{
		Version: 1, Source: "test", Hash: "test", IPv4: []string{"1.1.1.0/30"},
	}}, nil
}

type quickStartBenchmark struct{}

func (quickStartBenchmark) Run(_ context.Context, addresses []netip.Addr, _ func(benchmark.Progress)) ([]benchmark.Result, error) {
	results := make([]benchmark.Result, 0, len(addresses))
	for index, address := range addresses {
		results = append(results, benchmark.Result{
			IP: address, Family: 4, Attempts: 2, Successes: 2, TCPQualified: true,
			TLSVerified: true, Qualified: true, AvgLatency: time.Duration(index+1) * time.Millisecond,
			Score: 90 - float64(index),
		})
	}
	return results, nil
}

type quickStartPolicy struct{}

func (quickStartPolicy) Capabilities() proxy.Capabilities { return proxy.Capabilities{IPv4: true} }
func (quickStartPolicy) Apply(_ context.Context, policy proxy.DirectPolicy) (proxy.ApplyResult, error) {
	payload, _ := json.Marshal(policy)
	return proxy.ApplyResult{Receipts: []proxy.Receipt{{ID: "quickstart-test", Adapter: "test", Changed: true, Payload: payload}}}, nil
}
func (quickStartPolicy) Rollback(context.Context, proxy.ApplyResult) error { return nil }

func TestQuickStartPlanIsReadOnly(t *testing.T) {
	api, _, backend := newQuickStartTestAPI(t, false)
	plan, err := api.planQuickStart(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply || plan.PlanID == "" || plan.PhysicalPath.Interface != "Ethernet" {
		t.Fatalf("unexpected quick-start plan: %#v", plan)
	}
	if backend.replacements != 0 || backend.deletions != 0 || len(backend.routes) != 0 {
		t.Fatalf("read-only plan modified routes: %#v", backend)
	}
}

func TestQuickStartRejectsMissingAndExpiredPlans(t *testing.T) {
	api, _, _ := newQuickStartTestAPI(t, false)
	_, runErr := runQuickStartRequest(t, api, "missing", quickStartApplyOnce)
	assertQuickStartErrorCode(t, runErr, "plan_not_found")

	baseTime := api.now()
	plan, err := api.planQuickStart(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	api.now = func() time.Time { return baseTime.Add(quickStartPlanTTL) }
	_, runErr = runQuickStartRequest(t, api, plan.PlanID, quickStartApplyOnce)
	assertQuickStartErrorCode(t, runErr, "plan_expired")
}

func TestQuickStartRejectsNetworkAndConfigurationChanges(t *testing.T) {
	t.Run("network fingerprint", func(t *testing.T) {
		api, _, _ := newQuickStartTestAPI(t, false)
		fingerprint := "network-a"
		api.networkFingerprint = func(context.Context, time.Duration) (string, error) { return fingerprint, nil }
		plan, err := api.planQuickStart(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint = "network-b"
		_, runErr := runQuickStartRequest(t, api, plan.PlanID, quickStartApplyOnce)
		assertQuickStartErrorCode(t, runErr, "plan_stale")
	})
	t.Run("runtime config", func(t *testing.T) {
		api, runtimeState, _ := newQuickStartTestAPI(t, false)
		plan, err := api.planQuickStart(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		runtimeState.mutex.Lock()
		runtimeState.Config.Benchmark.Candidates++
		runtimeState.mutex.Unlock()
		_, runErr := runQuickStartRequest(t, api, plan.PlanID, quickStartApplyOnce)
		assertQuickStartErrorCode(t, runErr, "plan_stale")
	})
}

func TestQuickStartUsesSharedTaskLock(t *testing.T) {
	api, _, _ := newQuickStartTestAPI(t, false)
	plan, err := api.planQuickStart(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !api.setActiveCancel(func() {}) {
		t.Fatal("failed to reserve task lock")
	}
	defer api.clearActiveCancel()
	_, runErr := runQuickStartRequest(t, api, plan.PlanID, quickStartApplyOnce)
	assertQuickStartErrorCode(t, runErr, "conflict")
}

func TestConfigUpdateRejectsQuickStartWriteConflict(t *testing.T) {
	api, runtimeState, _ := newQuickStartTestAPI(t, false)
	runtimeState.ConfigPath = filepath.Join(t.TempDir(), "config.yaml")
	raw, err := json.Marshal(map[string]any{"config": runtimeState.View().Config})
	if err != nil {
		t.Fatal(err)
	}
	api.configurationMutex.Lock()
	_, updateErr := api.updateConfig(context.Background(), raw)
	api.configurationMutex.Unlock()
	assertQuickStartErrorCode(t, updateErr, "conflict")
}

func TestQuickStartVerificationFailureReportsRollback(t *testing.T) {
	api, _, backend := newQuickStartTestAPI(t, true)
	plan, err := api.planQuickStart(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := runQuickStartRequest(t, api, plan.PlanID, quickStartApplyOnce)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if result.Status != "rolled_back" || result.Error == "" || len(backend.routes) != 0 {
		t.Fatalf("verification failure was not reported as rolled back: result=%#v routes=%#v", result, backend.routes)
	}
}

func TestQuickStartEffectsExcludeReadOnlyMihomoDetection(t *testing.T) {
	cfg := config.Default()
	detections := map[string]proxy.Detection{
		cleanupAdapterGeneric: {Present: true, Manageable: true},
		cleanupAdapterMihomo:  {Present: true, Endpoint: "http://127.0.0.1:19097"},
	}
	effects := quickStartEffects(cfg, detections)
	if strings.Join(effects, ",") != "system_routes" {
		t.Fatalf("只读 Mihomo 发现不应进入写入影响清单：%#v", effects)
	}
	detection := detections[cleanupAdapterMihomo]
	detection.Manageable = true
	detections[cleanupAdapterMihomo] = detection
	effects = quickStartEffects(cfg, detections)
	if strings.Join(effects, ",") != "system_routes,mihomo_policy" {
		t.Fatalf("已启用 Mihomo 应进入写入影响清单：%#v", effects)
	}
}

func TestQuickStartPersistenceFailureDoesNotEnableMaintenance(t *testing.T) {
	api, runtimeState, _ := newQuickStartTestAPI(t, false)
	runtimeState.ConfigPath = filepath.Join(t.TempDir(), "config.yaml")
	plan, err := api.planQuickStart(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	api.saveConfig = func(string, config.Config) error { return errors.New("forced persistence failure") }
	result, runErr := runQuickStartRequest(t, api, plan.PlanID, quickStartApplyAndRemember)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if result.Status != "partial" || result.AutoMaintenanceEnabled || result.PersistenceWarning == "" {
		t.Fatalf("persistence failure was misreported: %#v", result)
	}
	if runtimeState.View().Config.Network.ManageRoutes {
		t.Fatal("runtime activated maintenance after persistence failure")
	}
}

func TestQuickStartPersistenceActivatesManagedSession(t *testing.T) {
	api, runtimeState, _ := newQuickStartTestAPI(t, false)
	runtimeState.ConfigPath = filepath.Join(t.TempDir(), "config.yaml")
	plan, err := api.planQuickStart(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := runQuickStartRequest(t, api, plan.PlanID, quickStartApplyAndRemember)
	if runErr != nil {
		t.Fatal(runErr)
	}
	view := runtimeState.View()
	if result.Status != "verified" || !result.AutoMaintenanceEnabled || !view.Config.Network.ManageRoutes || view.Config.Network.Interface != "Ethernet" {
		t.Fatalf("managed session was not activated: result=%#v config=%#v", result, view.Config.Network)
	}
	saved, err := config.Load(runtimeState.ConfigPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Network.ManageRoutes || saved.Network.GatewayIPv4 != "192.0.2.1" {
		t.Fatalf("managed config was not persisted: %#v", saved.Network)
	}
}

func newQuickStartTestAPI(t *testing.T, resolveFail bool) (*API, *Runtime, *quickStartRouteBackend) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.Benchmark.IPv4 = true
	cfg.Benchmark.IPv6 = false
	cfg.Benchmark.Candidates = 2
	cfg.Benchmark.DownloadTop = 1
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stateStore, err := store.Open(dataDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	backend := &quickStartRouteBackend{routes: map[string]cfnetwork.RouteSpec{}, resolveFail: resolveFail}
	routes, err := cfnetwork.NewRouteController(dataDir, backend, true, logger)
	if err != nil {
		t.Fatal(err)
	}
	runtimeState := &Runtime{Config: cfg, Store: stateStore, Routes: routes, RouteBackend: backend, Logger: logger}
	api, err := NewAPI(runtimeState)
	if err != nil {
		t.Fatal(err)
	}
	path := cfnetwork.PhysicalPath{Interface: "Ethernet", InterfaceIndex: 7, GatewayIPv4: "192.0.2.1", SourceIPv4: []string{"192.0.2.20"}}
	api.discoverPhysicalPath = func(context.Context, string, string, string, time.Duration) (cfnetwork.PhysicalPath, error) {
		return path, nil
	}
	api.networkFingerprint = func(context.Context, time.Duration) (string, error) { return "network-a", nil }
	detections := map[string]proxy.Detection{cleanupAdapterGeneric: {Present: true, Message: "test route backend"}}
	api.detectManagedAdapters = func(context.Context, cfnetwork.PhysicalPath) (map[string]proxy.Detection, error) {
		return detections, nil
	}
	api.buildManagedSession = func(confirmedPath cfnetwork.PhysicalPath, _ map[string]proxy.Detection) (RuntimeSession, error) {
		managedConfig := runtimeState.View().Config
		managedConfig.Network.Interface = confirmedPath.Interface
		managedConfig.Network.GatewayIPv4 = confirmedPath.GatewayIPv4
		managedConfig.Network.ManageRoutes = true
		runner, runnerErr := optimizer.NewRunner(managedConfig, quickStartRanges{}, quickStartBenchmark{}, stateStore, routes, confirmedPath, quickStartPolicy{}, logger)
		return RuntimeSession{Config: managedConfig, Runner: runner, PhysicalPath: confirmedPath}, runnerErr
	}
	api.now = func() time.Time { return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) }
	return api, runtimeState, backend
}

func runQuickStartRequest(t *testing.T, api *API, planID, mode string) (QuickStartResult, error) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"plan_id": planID, "mode": mode, "force_range_refresh": false})
	if err != nil {
		t.Fatal(err)
	}
	return api.runQuickStart(context.Background(), raw, nil)
}

func assertQuickStartErrorCode(t *testing.T, err error, expected string) {
	t.Helper()
	var protocolError *ipc.Error
	if !errors.As(err, &protocolError) || protocolError.Code != expected {
		t.Fatalf("got error %v, want IPC code %q", err, expected)
	}
}
