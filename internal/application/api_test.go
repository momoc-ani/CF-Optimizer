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

	"github.com/cf-optimizer/cf-optimizer/internal/acceleration"
	"github.com/cf-optimizer/cf-optimizer/internal/benchmark"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/ipc"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/optimizer"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy/guard"
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

type manualMappingBenchmark struct{}

func (manualMappingBenchmark) Run(context.Context, []netip.Addr, func(benchmark.Progress)) ([]benchmark.Result, error) {
	return []benchmark.Result{
		{IP: netip.MustParseAddr("1.1.1.1"), Family: 4, Attempts: 2, Successes: 2, TCPQualified: true, TLSVerified: true, Qualified: true, Score: 99},
		{IP: netip.MustParseAddr("1.1.1.2"), Family: 4, Attempts: 2, Successes: 2, TCPQualified: true, TLSVerified: true, Qualified: true, Score: 98},
	}, nil
}

type manualMappingPolicy struct {
	fail     bool
	policies []proxy.DirectPolicy
	started  chan struct{}
	block    <-chan struct{}
}

func (*manualMappingPolicy) Capabilities() proxy.Capabilities {
	return proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}
}

func (p *manualMappingPolicy) Apply(_ context.Context, policy proxy.DirectPolicy) (proxy.ApplyResult, error) {
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	if p.block != nil {
		<-p.block
	}
	if p.fail {
		return proxy.ApplyResult{}, errors.New("forced policy application failure")
	}
	p.policies = append(p.policies, policy)
	return proxy.ApplyResult{Receipts: []proxy.Receipt{{ID: "manual-mapping-test", Adapter: "test", Changed: true}}}, nil
}

func (*manualMappingPolicy) Rollback(context.Context, proxy.ApplyResult) error { return nil }

type manualMappingResolver struct{}

func (manualMappingResolver) Resolve(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
}

type manualMappingVerifier struct{}

func (manualMappingVerifier) VerifyPreflight(context.Context, []proxy.DomainMapping) error {
	return nil
}
func (manualMappingVerifier) VerifyApplied(context.Context, []proxy.DomainMapping) error { return nil }

type manualMappingDownloadTester struct{}

func (manualMappingDownloadTester) DiscoverProbeURL(context.Context, string, string) (string, error) {
	return "https://manual.example/probe.bin", nil
}

func (manualMappingDownloadTester) Measure(context.Context, string, string, string) (acceleration.DownloadResult, error) {
	return acceleration.DownloadResult{ProbeURL: "https://manual.example/probe.bin", Downloaded: 1 << 20, Duration: time.Second, Mbps: 40}, nil
}

func TestRunOptimizationUpdatesManualMappingOnlyAfterVerifiedApplication(t *testing.T) {
	t.Run("verified application", func(t *testing.T) {
		api, runtimeState, policy := newManualMappingRunAPI(t, false)
		report, err := api.RunOptimization(context.Background(), optimizer.RunOptions{ApplyPolicy: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !report.PolicyApplied || len(report.DomainAllocations) != 1 || report.DomainAllocations[0].AssignedAddress != "1.1.1.1" {
			t.Fatalf("verified optimization did not select the ranked mapping: %#v", report)
		}
		if len(policy.policies) != 1 || runtimeState.View().Config.Acceleration.ManualMappings["manual.example"] != "1.1.1.1" {
			t.Fatalf("verified mapping was not published to runtime config: policies=%#v config=%#v", policy.policies, runtimeState.View().Config.Acceleration.ManualMappings)
		}
		persisted, loadErr := config.Load(runtimeState.ConfigPath, "")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if persisted.Acceleration.ManualMappings["manual.example"] != "1.1.1.1" {
			t.Fatalf("verified mapping was not persisted: %#v", persisted.Acceleration.ManualMappings)
		}
	})

	t.Run("benchmark only", func(t *testing.T) {
		api, runtimeState, _ := newManualMappingRunAPI(t, false)
		if _, err := api.RunOptimization(context.Background(), optimizer.RunOptions{ApplyPolicy: false}, nil); err != nil {
			t.Fatal(err)
		}
		assertSavedManualMapping(t, runtimeState, "1.1.1.3")
	})

	t.Run("application failure", func(t *testing.T) {
		api, runtimeState, _ := newManualMappingRunAPI(t, true)
		if _, err := api.RunOptimization(context.Background(), optimizer.RunOptions{ApplyPolicy: true}, nil); err == nil {
			t.Fatal("policy application failure was not returned")
		}
		assertSavedManualMapping(t, runtimeState, "1.1.1.3")
	})
}

func newManualMappingRunAPI(t *testing.T, failPolicy bool) (*API, *Runtime, *manualMappingPolicy) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.Benchmark.IPv4 = true
	cfg.Benchmark.IPv6 = false
	cfg.Benchmark.Candidates = 2
	cfg.Benchmark.DownloadTop = 2
	cfg.Acceleration.ManualDomains = []string{"manual.example"}
	cfg.Acceleration.ManualMappings = map[string]string{"manual.example": "1.1.1.3"}
	configPath := filepath.Join(dataDir, "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	stateStore, err := store.Open(dataDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	policy := &manualMappingPolicy{fail: failPolicy}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := optimizer.NewRunner(
		cfg,
		configUpdateRanges{},
		manualMappingBenchmark{},
		stateStore,
		nil,
		cfnetwork.PhysicalPath{},
		policy,
		logger,
	)
	if err != nil {
		t.Fatal(err)
	}
	runner.SetDomainResolver(manualMappingResolver{})
	runner.SetDomainMappingVerifier(manualMappingVerifier{})
	runner.SetDomainDownloadTester(manualMappingDownloadTester{})
	runtimeState := &Runtime{Config: cfg, ConfigPath: configPath, Store: stateStore, Runner: runner, Logger: logger}
	api, err := NewAPI(runtimeState)
	if err != nil {
		t.Fatal(err)
	}
	return api, runtimeState, policy
}

func assertSavedManualMapping(t *testing.T, runtimeState *Runtime, expected string) {
	t.Helper()
	if actual := runtimeState.View().Config.Acceleration.ManualMappings["manual.example"]; actual != expected {
		t.Fatalf("runtime manual mapping changed: got %q want %q", actual, expected)
	}
	persisted, err := config.Load(runtimeState.ConfigPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if actual := persisted.Acceleration.ManualMappings["manual.example"]; actual != expected {
		t.Fatalf("persisted manual mapping changed: got %q want %q", actual, expected)
	}
}

func TestApplyManualDomainMappingPersistsDeferredResultWithoutPolicyAdapter(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.Acceleration.ManualDomains = []string{"manual.example"}
	cfg.Acceleration.ManualMappings = map[string]string{}
	configPath := filepath.Join(dataDir, "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	stateStore, err := store.Open(dataDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	address := "104.16.0.1"
	testedAt := time.Now().UTC()
	if err := stateStore.Update(func(state *store.State) error {
		state.DiscoveredDomains["manual.example"] = store.DomainDiscovery{
			Domain: "manual.example", Source: "manual", CloudflareVerified: true, PreflightVerified: true,
			DownloadVerified: true, DownloadMbps: 40, DownloadAddress: address, DownloadTestedAt: testedAt,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := optimizer.NewRunner(cfg, configUpdateRanges{}, configUpdateBenchmark{}, stateStore, nil, cfnetwork.PhysicalPath{}, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	runtimeState := &Runtime{
		Config: cfg, ConfigPath: configPath, Store: stateStore, Runner: runner,
		Ranges: ranges.NewCatalog(cfg.Ranges, dataDir), Logger: logger,
	}
	result, err := runtimeState.ApplyManualDomainMapping(context.Background(), "manual.example", address)
	if err != nil {
		t.Fatal(err)
	}
	if result.ApplyState != optimizer.ManualMappingApplyStateDeferred || result.PolicyRefreshed || stateStore.Snapshot().Policy != nil {
		t.Fatalf("manual mapping was not safely deferred: %#v", result)
	}
	persisted, err := config.Load(configPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Acceleration.ManualMappings["manual.example"] != address || runtimeState.View().Config.Acceleration.ManualMappings["manual.example"] != address {
		t.Fatalf("deferred mapping was not retained: persisted=%#v runtime=%#v", persisted.Acceleration.ManualMappings, runtimeState.View().Config.Acceleration.ManualMappings)
	}
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

func TestManualDomainMethodsRejectUnknownAndMissingParameters(t *testing.T) {
	api := &API{runtime: &Runtime{}}
	for _, method := range []string{"acceleration.domain_test", "acceleration.domain_apply"} {
		t.Run(method+" unknown", func(t *testing.T) {
			_, err := api.Handle(context.Background(), ipc.Request{Method: method, Params: json.RawMessage(`{"domain":"ani.momoc.top","address":"104.16.132.229","unexpected":true}`)}, nil)
			var ipcErr *ipc.Error
			if !errors.As(err, &ipcErr) || ipcErr.Code != "invalid_params" {
				t.Fatalf("unexpected IPC error: %#v", err)
			}
		})
		t.Run(method+" missing", func(t *testing.T) {
			_, err := api.Handle(context.Background(), ipc.Request{Method: method, Params: json.RawMessage(`{"domain":"ani.momoc.top"}`)}, nil)
			var ipcErr *ipc.Error
			if !errors.As(err, &ipcErr) || ipcErr.Code != "invalid_params" {
				t.Fatalf("unexpected IPC error: %#v", err)
			}
		})
	}
	for _, method := range []string{"acceleration.manual_domain_add", "acceleration.manual_domain_remove"} {
		t.Run(method+" unknown", func(t *testing.T) {
			_, err := api.Handle(context.Background(), ipc.Request{Method: method, Params: json.RawMessage(`{"domain":"ani.momoc.top","unexpected":true}`)}, nil)
			var ipcErr *ipc.Error
			if !errors.As(err, &ipcErr) || ipcErr.Code != "invalid_params" {
				t.Fatalf("unexpected IPC error: %#v", err)
			}
		})
		t.Run(method+" missing", func(t *testing.T) {
			_, err := api.Handle(context.Background(), ipc.Request{Method: method, Params: json.RawMessage(`{}`)}, nil)
			var ipcErr *ipc.Error
			if !errors.As(err, &ipcErr) || ipcErr.Code != "invalid_params" {
				t.Fatalf("unexpected IPC error: %#v", err)
			}
		})
	}
}

func TestManualDomainAddVerifiesInBackgroundAndPersistsMapping(t *testing.T) {
	api, runtimeState, _ := newManualMappingRunAPI(t, false)
	if _, err := api.RunOptimization(context.Background(), optimizer.RunOptions{ApplyPolicy: true}, nil); err != nil {
		t.Fatal(err)
	}

	result, err := api.Handle(context.Background(), ipc.Request{Method: "acceleration.manual_domain_add", Params: json.RawMessage(`{"domain":"added.example"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := result.(ManualDomainOperation)
	if !ok || operation.State != manualDomainStatePending || operation.Domain != "added.example" {
		t.Fatalf("unexpected immediate operation: %#v", result)
	}
	completed := waitForManualDomainOperation(t, api, "added.example", manualDomainStateVerified)
	if completed.Message == "" {
		t.Fatal("verified operation should include a status message")
	}

	persisted, err := config.Load(runtimeState.ConfigPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if !containsNormalizedDomain(persisted.Acceleration.ManualDomains, "added.example") || persisted.Acceleration.ManualMappings["added.example"] == "" {
		t.Fatalf("verified domain was not fully persisted: %#v", persisted.Acceleration)
	}
	if !policyContainsManualDomain(runtimeState.Store.Snapshot().Policy, "added.example") {
		t.Fatal("verified policy does not contain the added domain")
	}
}

func TestManualDomainRemoveVerifiesPolicyBeforeFinalConfigCleanup(t *testing.T) {
	api, runtimeState, policy := newManualMappingRunAPI(t, false)
	if _, err := api.RunOptimization(context.Background(), optimizer.RunOptions{ApplyPolicy: true}, nil); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	policy.started = started
	policy.block = release

	result, err := api.Handle(context.Background(), ipc.Request{Method: "acceleration.manual_domain_remove", Params: json.RawMessage(`{"domain":"manual.example"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := result.(ManualDomainOperation)
	if !ok || operation.State != manualDomainStateRemoving {
		t.Fatalf("unexpected immediate operation: %#v", result)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background policy removal did not start")
	}
	if persisted, loadErr := config.Load(runtimeState.ConfigPath, ""); loadErr != nil || !containsNormalizedDomain(persisted.Acceleration.ManualDomains, "manual.example") {
		t.Fatalf("domain was removed before background verification: config=%#v err=%v", persisted.Acceleration, loadErr)
	}
	close(release)
	waitForManualDomainOperation(t, api, "manual.example", manualDomainStateRemoved)

	persisted, err := config.Load(runtimeState.ConfigPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if containsNormalizedDomain(persisted.Acceleration.ManualDomains, "manual.example") || persisted.Acceleration.ManualMappings["manual.example"] != "" {
		t.Fatalf("domain configuration was not cleaned after verification: %#v", persisted.Acceleration)
	}
	if policyContainsManualDomain(runtimeState.Store.Snapshot().Policy, "manual.example") {
		t.Fatal("verified policy still contains the removed domain")
	}
}

func TestManualDomainRemoveFailureKeepsConfigAndPreviousPolicy(t *testing.T) {
	api, runtimeState, policy := newManualMappingRunAPI(t, false)
	if _, err := api.RunOptimization(context.Background(), optimizer.RunOptions{ApplyPolicy: true}, nil); err != nil {
		t.Fatal(err)
	}
	policy.fail = true

	if _, err := api.Handle(context.Background(), ipc.Request{Method: "acceleration.manual_domain_remove", Params: json.RawMessage(`{"domain":"manual.example"}`)}, nil); err != nil {
		t.Fatal(err)
	}
	waitForManualDomainOperation(t, api, "manual.example", manualDomainStateFailed)

	persisted, err := config.Load(runtimeState.ConfigPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if !containsNormalizedDomain(persisted.Acceleration.ManualDomains, "manual.example") || persisted.Acceleration.ManualMappings["manual.example"] == "" {
		t.Fatalf("failed removal changed persisted configuration: %#v", persisted.Acceleration)
	}
	if !policyContainsManualDomain(runtimeState.Store.Snapshot().Policy, "manual.example") {
		t.Fatal("failed removal discarded the previous verified policy")
	}
}

func TestManualDomainRemovePersistenceFailureRestoresPreviousPolicy(t *testing.T) {
	api, runtimeState, _ := newManualMappingRunAPI(t, false)
	if _, err := api.RunOptimization(context.Background(), optimizer.RunOptions{ApplyPolicy: true}, nil); err != nil {
		t.Fatal(err)
	}
	api.saveConfig = func(string, config.Config) error { return errors.New("forced configuration save failure") }

	if _, err := api.Handle(context.Background(), ipc.Request{Method: "acceleration.manual_domain_remove", Params: json.RawMessage(`{"domain":"manual.example"}`)}, nil); err != nil {
		t.Fatal(err)
	}
	waitForManualDomainOperation(t, api, "manual.example", manualDomainStateFailed)

	persisted, err := config.Load(runtimeState.ConfigPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if !containsNormalizedDomain(persisted.Acceleration.ManualDomains, "manual.example") || persisted.Acceleration.ManualMappings["manual.example"] == "" {
		t.Fatalf("persistence failure changed the saved domain: %#v", persisted.Acceleration)
	}
	if !policyContainsManualDomain(runtimeState.Store.Snapshot().Policy, "manual.example") {
		t.Fatal("persistence failure did not restore the previous verified policy")
	}
}

func waitForManualDomainOperation(t *testing.T, api *API, domain, expectedState string) ManualDomainOperation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, operation := range api.manualDomainOperations() {
			if operation.Domain != domain {
				continue
			}
			if operation.State == expectedState {
				return operation
			}
			if operation.State == manualDomainStateFailed && expectedState != manualDomainStateFailed {
				t.Fatalf("manual domain operation failed: %#v", operation)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("manual domain %s did not reach %s: %#v", domain, expectedState, api.manualDomainOperations())
	return ManualDomainOperation{}
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

func TestStartupRecoveryStatusAllowsReadsAndRejectsMutations(t *testing.T) {
	dataDir := t.TempDir()
	stateStore, err := store.Open(dataDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DataDir = dataDir
	api, err := NewAPI(&Runtime{Config: cfg, Store: stateStore})
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, time.August, 7, 0, 0, 0, 0, time.UTC)
	api.SetStartupStatus(StartupStatus{Stage: "recovering_routes", Message: "正在恢复中断的路由事务", StartedAt: startedAt})

	response, err := api.Handle(context.Background(), ipc.Request{Method: "system.status"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	startup, ok := response.(map[string]any)["startup"].(StartupStatus)
	if !ok || startup.Ready || startup.Stage != "recovering_routes" || !startup.StartedAt.Equal(startedAt) {
		t.Fatalf("unexpected startup status: %#v", response.(map[string]any)["startup"])
	}
	if _, err := api.Handle(context.Background(), ipc.Request{Method: "logs.tail", Params: json.RawMessage(`{"lines":10}`)}, nil); err != nil {
		t.Fatalf("read-only logs request was blocked: %v", err)
	}
	_, err = api.Handle(context.Background(), ipc.Request{Method: "optimizer.run", Params: json.RawMessage(`{}`)}, nil)
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != "service_initializing" {
		t.Fatalf("unexpected startup mutation error: %#v", err)
	}

	api.SetStartupStatus(StartupStatus{Ready: true, Stage: "ready", StartedAt: startedAt})
	if _, err := api.Handle(context.Background(), ipc.Request{Method: "optimizer.cancel"}, nil); err != nil {
		t.Fatalf("ready API still rejected mutation: %v", err)
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

func TestSystemStatusIncludesCompactNodePoolAndCheckpoint(t *testing.T) {
	stateStore, err := store.Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC().Add(-time.Hour)
	validUntil := time.Now().UTC().Add(time.Hour)
	if err := stateStore.Update(func(state *store.State) error {
		state.NodePool = &store.NodePoolSnapshot{Version: store.NodePoolSchemaVersion, ID: "pool-1", CreatedAt: createdAt, ValidUntil: validUntil, Candidates: []benchmark.Result{{IP: netip.MustParseAddr("1.1.1.1")}, {IP: netip.MustParseAddr("1.1.1.2")}}}
		state.Optimization = &store.OptimizationCheckpoint{Version: store.OptimizationCheckpointVersion, RunID: "run-1", CurrentStage: "apply_verify", LastError: "forced failure", UpdatedAt: createdAt}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	api, err := NewAPI(&Runtime{Store: stateStore})
	if err != nil {
		t.Fatal(err)
	}

	response := api.systemStatus()["state"].(statusState)
	if response.NodePool == nil || response.NodePool.ID != "pool-1" || response.NodePool.Candidates != 2 || response.NodePool.Stale {
		t.Fatalf("unexpected compact node pool status: %#v", response.NodePool)
	}
	if response.Optimization == nil || response.Optimization.Stage != "apply_verify" || response.Optimization.LastError != "forced failure" {
		t.Fatalf("unexpected optimization checkpoint status: %#v", response.Optimization)
	}
}

func TestSystemStatusIncludesIsolatedPolicyGuardStatus(t *testing.T) {
	stateStore, err := store.Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewAPI(&Runtime{Store: stateStore})
	if err != nil {
		t.Fatal(err)
	}
	reasons := []string{"活动配置缺少规则"}
	api.SetPolicyGuardStatus(guard.Status{ID: "mihomo", State: guard.StateDrifted, Activity: guard.ActivityActive, DriftReasons: reasons, Transition: 2})
	reasons[0] = "调用方修改"

	response := api.systemStatus()
	statuses, ok := response["policy_guards"].(map[string]guard.Status)
	if !ok {
		t.Fatalf("unexpected policy guard response: %#v", response["policy_guards"])
	}
	status := statuses["mihomo"]
	if status.State != guard.StateDrifted || len(status.DriftReasons) != 1 || status.DriftReasons[0] != "活动配置缺少规则" {
		t.Fatalf("policy guard status was not isolated: %#v", status)
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
	next.Acceleration.ManualMappings = map[string]string{"comfy.momoc.top": "104.19.208.241"}
	next.Acceleration.ManualDomains = []string{}
	next.Acceleration.ManualDownloadTest = !next.Acceleration.ManualDownloadTest
	result := updateConfigForTest(t, api, next)
	if !result["saved"] || !result["hot_applied"] || result["policy_refreshed"] || result["restart_required"] {
		t.Fatalf("unexpected update result: %#v", result)
	}
	if domains := runtimeState.View().Config.Acceleration.ManualDomains; len(domains) != 0 {
		t.Fatalf("runtime retained cleared manual domains: %#v", domains)
	}
	if mappings := runtimeState.View().Config.Acceleration.ManualMappings; len(mappings) != 0 {
		t.Fatalf("runtime retained mappings for removed manual domains: %#v", mappings)
	}
	if runtimeState.View().Config.Acceleration.ManualDownloadTest != next.Acceleration.ManualDownloadTest {
		t.Fatal("runtime did not apply the manual domain download test switch")
	}
	response, err := api.Handle(context.Background(), ipc.Request{Method: "config.get"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	persisted := response.(config.Config)
	if len(persisted.Acceleration.ManualDomains) != 0 {
		t.Fatalf("config.get returned stale manual domains: %#v", persisted.Acceleration.ManualDomains)
	}
	if len(persisted.Acceleration.ManualMappings) != 0 {
		t.Fatalf("config.get returned stale manual mappings: %#v", persisted.Acceleration.ManualMappings)
	}
	if persisted.Acceleration.ManualDownloadTest != next.Acceleration.ManualDownloadTest {
		t.Fatal("config.get returned a stale manual domain download test switch")
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
