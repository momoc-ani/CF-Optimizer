package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
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

type configUpdateRanges struct{}

func (configUpdateRanges) Update(context.Context, bool) (ranges.UpdateResult, error) {
	return ranges.UpdateResult{Snapshot: ranges.Snapshot{Version: 1, Source: "test", Hash: "test", IPv4: []string{"1.1.1.0/24"}}}, nil
}

type configUpdateBenchmark struct{}

func (configUpdateBenchmark) Run(context.Context, []netip.Addr, func(benchmark.Progress)) ([]benchmark.Result, error) {
	return nil, nil
}

func TestDecodeStrictRejectsUnknownFieldAndTrailingValue(t *testing.T) {
	var target struct {
		Value bool `json:"value"`
	}
	if err := decodeStrict(json.RawMessage(`{"unknown":true}`), &target); err == nil {
		t.Fatal("expected unknown field error")
	}
	if err := decodeStrict(json.RawMessage(`{"value":true} {}`), &target); err == nil {
		t.Fatal("expected trailing value error")
	}
}

func TestClearDiscoveredDomainsRejectsUnknownParameters(t *testing.T) {
	api := &API{runtime: &Runtime{}}
	_, err := api.Handle(context.Background(), ipc.Request{Method: "acceleration.clear_discovered", Params: json.RawMessage(`{"unexpected":true}`)}, nil)
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != "invalid_params" {
		t.Fatalf("unexpected IPC error: %#v", err)
	}
}

func TestActiveEventIsClonedAndClearedWithRun(t *testing.T) {
	api := &API{}
	progress := benchmark.Progress{Completed: 4, Total: 12}
	event := optimizer.Event{RunID: "run-1", Type: "benchmark.progress", Progress: &progress}
	api.setActiveEvent(event)

	progress.Completed = 9
	actual := cloneEvent(api.activeEvent)
	if actual == nil || actual.Progress == nil || actual.Progress.Completed != 4 {
		t.Fatalf("active event was not isolated from caller mutation: %#v", actual)
	}

	api.clearActiveCancel()
	if api.activeEvent != nil {
		t.Fatal("active event should be cleared after the run finishes")
	}
}

func TestNewestHistoryFirstDoesNotMutateStoredOrder(t *testing.T) {
	history := []store.RunSummary{{ID: "old"}, {ID: "new"}}
	result := newestHistoryFirst(history)
	if len(result) != 2 || result[0].ID != "new" || result[1].ID != "old" {
		t.Fatalf("unexpected history order: %#v", result)
	}
	if history[0].ID != "old" || history[1].ID != "new" {
		t.Fatalf("source history was mutated: %#v", history)
	}
}

func TestLatestBenchmarkReturnsNewestPersistedSuccessfulDetail(t *testing.T) {
	stateStore, err := store.Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	results := []benchmark.Result{
		{IP: netip.MustParseAddr("104.25.250.104"), Family: 4, Qualified: true, Score: 93.31},
		{IP: netip.MustParseAddr("104.25.250.105"), Family: 4, Qualified: true, Score: 92.31},
		{IP: netip.MustParseAddr("104.25.250.106"), Family: 4, Qualified: true, Score: 91.31},
	}
	payload, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.SaveRunDetail("successful-run", payload, time.Hour); err != nil {
		t.Fatal(err)
	}
	finishedAt := time.Date(2026, time.August, 5, 0, 39, 44, 0, time.UTC)
	if err := stateStore.Update(func(state *store.State) error {
		state.History = []store.RunSummary{
			{ID: "successful-run", FinishedAt: finishedAt},
			{ID: "failed-run", FinishedAt: finishedAt.Add(time.Minute), Error: "policy verification failed"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtimeState := &Runtime{Store: stateStore, Config: config.Default()}
	runtimeState.Config.Benchmark.DownloadTop = 2
	api, err := NewAPI(runtimeState)
	if err != nil {
		t.Fatal(err)
	}

	response, err := api.Handle(context.Background(), ipc.Request{Method: "history.latest", Params: json.RawMessage(`{}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	latest, ok := response.(LatestBenchmark)
	if !ok || latest.RunID != "successful-run" || !latest.FinishedAt.Equal(finishedAt) || len(latest.Results) != 2 || latest.Results[0].IP.String() != "104.25.250.104" || latest.Results[1].IP.String() != "104.25.250.105" {
		t.Fatalf("unexpected latest benchmark: %#v", response)
	}
}

func TestTrimBenchmarkResultsKeepsPersistedOrder(t *testing.T) {
	results := []benchmark.Result{{Score: 3}, {Score: 2}, {Score: 1}}
	trimmed := trimBenchmarkResults(results, 2)
	if len(trimmed) != 2 || trimmed[0].Score != 3 || trimmed[1].Score != 2 {
		t.Fatalf("unexpected trimmed results: %#v", trimmed)
	}
	if len(results) != 3 {
		t.Fatal("trimming changed persisted result slice")
	}
}

func TestLatestBenchmarkRejectsUnknownParameters(t *testing.T) {
	api := &API{runtime: &Runtime{}}
	_, err := api.Handle(context.Background(), ipc.Request{Method: "history.latest", Params: json.RawMessage(`{"unexpected":true}`)}, nil)
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != "invalid_params" {
		t.Fatalf("unexpected IPC error: %#v", err)
	}
}

func TestSystemStatusExcludesLargeInternalState(t *testing.T) {
	stateStore, err := store.Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	nodes := make(map[string]store.NodeStats, 5_000)
	for index := 0; index < 5_000; index++ {
		nodes[fmt.Sprintf("node-%05d-with-historical-statistics", index)] = store.NodeStats{Attempts: index + 1}
	}
	current := &store.Selection{IP: "104.21.94.176", Family: 4, PolicyVerified: true}
	largeReceipts := json.RawMessage(`{"backup":"` + strings.Repeat("x", 2<<20) + `"}`)
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = current
		state.Nodes = nodes
		state.History = []store.RunSummary{{ID: "run-sensitive-history"}}
		state.DiscoveredDomains["ani.momoc.top"] = store.DomainDiscovery{Domain: "ani.momoc.top", Active: true}
		state.Policy = &store.PolicySnapshot{Receipts: largeReceipts, AppliedAt: time.Now().UTC()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	api, err := NewAPI(&Runtime{Store: stateStore})
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.Handle(context.Background(), ipc.Request{Method: "system.status"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= 64<<10 {
		t.Fatalf("system.status response is unexpectedly large: %d bytes", len(encoded))
	}
	responseText := string(encoded)
	for _, internalField := range []string{`"history"`, `"nodes"`, `"discovered_domains"`, `"policy"`, `"receipts"`, "run-sensitive-history"} {
		if strings.Contains(responseText, internalField) {
			t.Fatalf("system.status exposed internal field %s", internalField)
		}
	}
	if !strings.Contains(responseText, "104.21.94.176") {
		t.Fatal("system.status omitted the current verified selection")
	}
}

func TestSystemStatusIncludesIsolatedSchedulePromise(t *testing.T) {
	stateStore, err := store.Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewAPI(&Runtime{Store: stateStore})
	if err != nil {
		t.Fatal(err)
	}
	next := time.Date(2026, time.August, 2, 18, 30, 0, 0, time.UTC)
	api.SetScheduleStatus(ScheduleStatus{
		Enabled: true, Interval: "6h0m0s", NextScheduledAt: &next, Trigger: "interval",
	})
	next = next.Add(time.Hour)

	response := api.systemStatus()
	status, ok := response["schedule"].(ScheduleStatus)
	if !ok {
		t.Fatalf("unexpected schedule response: %#v", response["schedule"])
	}
	if !status.Enabled || status.Interval != "6h0m0s" || status.Trigger != "interval" {
		t.Fatalf("unexpected schedule status: %#v", status)
	}
	want := time.Date(2026, time.August, 2, 18, 30, 0, 0, time.UTC)
	if status.NextScheduledAt == nil || !status.NextScheduledAt.Equal(want) {
		t.Fatalf("unexpected next scheduled time: %#v", status.NextScheduledAt)
	}
}

func TestSystemStatusReportsPolicyAvailability(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		coordinator *proxy.Coordinator
		want        bool
	}{
		{name: "未激活适配器", want: false},
		{name: "已激活适配器", coordinator: new(proxy.Coordinator), want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stateStore, err := store.Open(t.TempDir(), 10)
			if err != nil {
				t.Fatal(err)
			}
			api, err := NewAPI(&Runtime{Store: stateStore, ProxyCoordinator: testCase.coordinator})
			if err != nil {
				t.Fatal(err)
			}

			response := api.systemStatus()
			available, ok := response["policy_available"].(bool)
			if !ok || available != testCase.want {
				t.Fatalf("unexpected policy availability: value=%#v want=%t", response["policy_available"], testCase.want)
			}
		})
	}
}

func TestConfigUpdateHotAppliesClearedManualDomains(t *testing.T) {
	api, runtimeState := newConfigUpdateTestAPI(t)
	next := runtimeState.View().Config
	next.Acceleration.ManualDomains = []string{}
	result := updateConfigForTest(t, api, next)
	if !result["saved"] || !result["hot_applied"] || result["policy_refreshed"] || result["restart_required"] {
		t.Fatalf("unexpected update result: %#v", result)
	}
	if domains := runtimeState.View().Config.Acceleration.ManualDomains; len(domains) != 0 {
		t.Fatalf("runtime retained cleared manual domains: %#v", domains)
	}
	response, err := api.Handle(context.Background(), ipc.Request{Method: "config.get"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	persisted := response.(config.Config)
	if len(persisted.Acceleration.ManualDomains) != 0 {
		t.Fatalf("config.get returned stale manual domains: %#v", persisted.Acceleration.ManualDomains)
	}
	domainResponse, err := api.Handle(context.Background(), ipc.Request{Method: "acceleration.domains"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if domains := domainResponse.(DomainDiscoveryResult).Domains; len(domains) != 0 {
		t.Fatalf("acceleration.domains retained the removed manual domain: %#v", domains)
	}
}

func TestConfigGetReturnsHotReloadedSettings(t *testing.T) {
	api, runtimeState := newConfigUpdateTestAPI(t)
	next := runtimeState.View().Config
	next.Schedule.Interval = config.Duration(7 * time.Hour)
	result := updateConfigForTest(t, api, next)
	if !result["saved"] || !result["hot_applied"] || result["restart_required"] {
		t.Fatalf("unexpected update result: %#v", result)
	}
	if runtimeState.View().Config.Schedule.Interval != next.Schedule.Interval {
		t.Fatal("saved setting was not hot-reloaded into the active runtime")
	}
	response, err := api.Handle(context.Background(), ipc.Request{Method: "config.get"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if persisted := response.(config.Config); persisted.Schedule.Interval != next.Schedule.Interval {
		t.Fatalf("config.get did not return the saved desired config: %#v", persisted.Schedule)
	}
}

func TestConfigUpdateDefaultsBlankDownloadURL(t *testing.T) {
	api, runtimeState := newConfigUpdateTestAPI(t)
	next := runtimeState.View().Config
	next.Benchmark.DownloadURL = ""
	next.Benchmark.DownloadMaxBytes = 0
	result := updateConfigForTest(t, api, next)
	if !result["saved"] || result["restart_required"] {
		t.Fatalf("unexpected update result: %#v", result)
	}
	persisted, err := config.Load(runtimeState.ConfigPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Benchmark.DownloadURL != config.DefaultDownloadURL || persisted.Benchmark.DownloadMaxBytes != config.DefaultDownloadMaxBytes {
		t.Fatalf("blank download settings were not defaulted: %#v", persisted.Benchmark)
	}
}

func TestConfigUpdateRestoresPersistedConfigWhenHotReloadFails(t *testing.T) {
	api, runtimeState := newConfigUpdateTestAPI(t)
	before := runtimeState.View().Config
	api.reloadConfig = func(context.Context, config.Config, bool) (bool, error) {
		return false, errors.New("runtime reload failed")
	}
	next := before
	next.Schedule.Interval = config.Duration(7 * time.Hour)
	payload, err := json.Marshal(map[string]any{"config": next})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Handle(context.Background(), ipc.Request{Method: "config.update", Params: payload}, nil); err == nil {
		t.Fatal("hot reload failure was not returned")
	}
	persisted, err := config.Load(runtimeState.ConfigPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Schedule.Interval != before.Schedule.Interval {
		t.Fatalf("failed hot reload left new disk configuration: %s", persisted.Schedule.Interval)
	}
	if runtimeState.View().Config.Schedule.Interval != before.Schedule.Interval {
		t.Fatal("failed hot reload changed the active runtime")
	}
}

func TestConfigUpdateRefreshesPolicyForPhysicalPathChange(t *testing.T) {
	api, runtimeState := newConfigUpdateTestAPI(t)
	refreshRequested := false
	api.reloadConfig = func(_ context.Context, next config.Config, refreshPolicy bool) (bool, error) {
		refreshRequested = refreshPolicy
		runtimeState.mutex.Lock()
		runtimeState.Config = next
		runtimeState.notifyConfigChangedLocked()
		runtimeState.mutex.Unlock()
		return refreshPolicy, nil
	}
	next := runtimeState.View().Config
	next.Network.Interface = "Ethernet 3"
	next.Network.GatewayIPv4 = "192.168.15.1"
	next.Network.ManageRoutes = true
	result := updateConfigForTest(t, api, next)
	if !refreshRequested || !result["policy_refreshed"] || !result["hot_applied"] || result["restart_required"] {
		t.Fatalf("physical path update did not request a verified policy refresh: result=%#v requested=%t", result, refreshRequested)
	}
}

func TestConfigUpdateSavesDomainsWithoutAdapterWhenPolicySnapshotExists(t *testing.T) {
	api, runtimeState := newConfigUpdateTestAPI(t)
	if err := runtimeState.Store.Update(func(state *store.State) error {
		state.Policy = &store.PolicySnapshot{Domains: []string{"ani.momoc.top"}, Receipts: json.RawMessage(`{"receipts":[]}`)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := runtimeState.View().Config
	next.Acceleration.ManualDomains = []string{}
	result := updateConfigForTest(t, api, next)
	if !result["saved"] || !result["hot_applied"] || result["policy_refreshed"] || result["restart_required"] {
		t.Fatalf("unexpected update result: %#v", result)
	}
	if domains := runtimeState.View().Config.Acceleration.ManualDomains; len(domains) != 0 {
		t.Fatalf("runtime did not retain saved manual domains: %#v", domains)
	}
	persisted, err := config.Load(runtimeState.ConfigPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if domains := persisted.Acceleration.ManualDomains; len(domains) != 0 {
		t.Fatalf("persisted config omitted saved manual domains: %#v", domains)
	}
	policy := runtimeState.Store.Snapshot().Policy
	if policy == nil || len(policy.Domains) != 1 || policy.Domains[0] != "ani.momoc.top" {
		t.Fatalf("unrefreshable policy snapshot should remain available for cleanup: %#v", policy)
	}
}

func TestConfigUpdatePublishesActiveEventAndSupportsCancellation(t *testing.T) {
	api, runtimeState := newConfigUpdateTestAPI(t)
	next := runtimeState.View().Config
	next.Network.Interface = "Ethernet 3"
	raw, err := json.Marshal(map[string]any{"config": next})
	if err != nil {
		t.Fatal(err)
	}
	reloadStarted := make(chan struct{})
	api.reloadConfig = func(ctx context.Context, _ config.Config, _ bool) (bool, error) {
		close(reloadStarted)
		<-ctx.Done()
		return false, ctx.Err()
	}
	updateResult := make(chan error, 1)
	go func() {
		_, updateErr := api.updateConfig(context.Background(), raw)
		updateResult <- updateErr
	}()
	select {
	case <-reloadStarted:
	case <-time.After(time.Second):
		t.Fatal("configuration reload did not start")
	}
	status := api.systemStatus()
	activeEvent, ok := status["active_event"].(*optimizer.Event)
	if !ok || activeEvent == nil || activeEvent.Stage != "config" {
		t.Fatalf("configuration reload active event is missing: %#v", status["active_event"])
	}
	cancelled, err := api.cancelOptimizer()
	if err != nil || !cancelled["cancelled"] {
		t.Fatalf("configuration reload cancellation failed: result=%#v error=%v", cancelled, err)
	}
	select {
	case updateErr := <-updateResult:
		if !errors.Is(updateErr, context.Canceled) {
			t.Fatalf("configuration update error = %v, want context cancellation", updateErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled configuration update did not return")
	}
	activeEvent, _ = api.systemStatus()["active_event"].(*optimizer.Event)
	if activeEvent != nil {
		t.Fatalf("configuration reload active event was not cleared: %#v", activeEvent)
	}
}

func newConfigUpdateTestAPI(t *testing.T) (*API, *Runtime) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.Benchmark.IPv4 = true
	cfg.Benchmark.IPv6 = false
	cfg.Acceleration.ManualDomains = []string{"ani.momoc.top"}
	configPath := filepath.Join(dataDir, "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	stateStore, err := store.Open(dataDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := optimizer.NewRunner(cfg, configUpdateRanges{}, configUpdateBenchmark{}, stateStore, nil, cfnetwork.PhysicalPath{}, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	runtimeState := &Runtime{Config: cfg, ConfigPath: configPath, Store: stateStore, Runner: runner, Logger: logger}
	api, err := NewAPI(runtimeState)
	if err != nil {
		t.Fatal(err)
	}
	api.reloadConfig = func(_ context.Context, next config.Config, _ bool) (bool, error) {
		runtimeState.mutex.Lock()
		runtimeState.Config = next
		runtimeState.notifyConfigChangedLocked()
		runtimeState.mutex.Unlock()
		return false, nil
	}
	return api, runtimeState
}

func updateConfigForTest(t *testing.T, api *API, next config.Config) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"config": next})
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.Handle(context.Background(), ipc.Request{Method: "config.update", Params: raw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return response.(map[string]bool)
}
