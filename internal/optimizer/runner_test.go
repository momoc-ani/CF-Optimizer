package optimizer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/acceleration"
	"github.com/cf-optimizer/cf-optimizer/internal/benchmark"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/ranges"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

type staticRanges struct{ snapshot ranges.Snapshot }

func (s staticRanges) Update(context.Context, bool) (ranges.UpdateResult, error) {
	return ranges.UpdateResult{Snapshot: s.snapshot}, nil
}

type blockingRanges struct {
	snapshot ranges.Snapshot
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
}

// Update 阻塞首次网段读取，用于稳定复现策略刷新与手动优选的竞争。
func (s *blockingRanges) Update(ctx context.Context, _ bool) (ranges.UpdateResult, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return ranges.UpdateResult{}, ctx.Err()
	case <-s.release:
		return ranges.UpdateResult{Snapshot: s.snapshot}, nil
	}
}

type staticBenchmark struct{}

func (staticBenchmark) Run(_ context.Context, addresses []netip.Addr, progress func(benchmark.Progress)) ([]benchmark.Result, error) {
	results := make([]benchmark.Result, 0, len(addresses))
	for index, address := range addresses {
		if progress != nil {
			progress(benchmark.Progress{Stage: benchmark.StageTCP, Completed: index + 1, Total: len(addresses), IP: address.String(), Qualified: index + 1})
		}
		results = append(results, benchmark.Result{
			IP: address, Family: 4, Attempts: 2, Successes: 2, TCPQualified: true, TLSVerified: true,
			Qualified: true, AvgLatency: time.Duration(index+1) * time.Millisecond, Score: 90 - float64(index),
		})
	}
	return results, nil
}

type unqualifiedBenchmark struct{}

func (unqualifiedBenchmark) Run(_ context.Context, addresses []netip.Addr, _ func(benchmark.Progress)) ([]benchmark.Result, error) {
	results := make([]benchmark.Result, 0, len(addresses))
	for _, address := range addresses {
		results = append(results, benchmark.Result{
			IP: address, Family: 4, Attempts: 2, Successes: 2, TCPQualified: true, TLSVerified: true,
			Qualified: false, Error: "download returned 429 Too Many Requests",
		})
	}
	return results, nil
}

type recordingPolicy struct {
	capabilities   proxy.Capabilities
	rejectedDomain string
	policies       []proxy.DirectPolicy
	rollbacks      []proxy.ApplyResult
}

type candidateRejectingPolicy struct {
	recordingPolicy
	rejectedAddress string
	rejectAll       bool
	rejectFirst     bool
}

type failingPolicy struct{}

func (failingPolicy) Capabilities() proxy.Capabilities {
	return proxy.Capabilities{IPv4: true}
}

func (failingPolicy) Apply(context.Context, proxy.DirectPolicy) (proxy.ApplyResult, error) {
	return proxy.ApplyResult{}, errors.New("policy refresh failed")
}

func (failingPolicy) Rollback(context.Context, proxy.ApplyResult) error { return nil }

type guardedRecordingPolicy struct {
	recordingPolicy
	events      []string
	guardActive bool
}

func (p *guardedRecordingPolicy) BeginBenchmarkGuard(context.Context, proxy.DirectPolicy, []netip.Addr) (proxy.BenchmarkGuardResult, error) {
	p.events = append(p.events, "guard_begin")
	p.guardActive = true
	return proxy.BenchmarkGuardResult{
		Receipts: []proxy.Receipt{{ID: "benchmark-guard", Adapter: "test", Changed: true}},
		Evidence: []proxy.BenchmarkPathEvidence{{Target: "1.1.1.1", SocketBound: true, ProxyObserved: true, DirectVerified: true, Verification: "test_direct"}},
	}, nil
}

func (p *guardedRecordingPolicy) EndBenchmarkGuard(context.Context, proxy.BenchmarkGuardResult) error {
	p.events = append(p.events, "guard_end")
	p.guardActive = false
	return nil
}

func (p *guardedRecordingPolicy) Apply(ctx context.Context, policy proxy.DirectPolicy) (proxy.ApplyResult, error) {
	if p.guardActive {
		return proxy.ApplyResult{}, errors.New("final policy applied before benchmark guard rollback")
	}
	p.events = append(p.events, "policy_apply")
	return p.recordingPolicy.Apply(ctx, policy)
}

type staticDomainResolver struct {
	addresses []netip.Addr
	err       error
}

func (r staticDomainResolver) Resolve(context.Context, string) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), r.addresses...), r.err
}

type selectiveDomainVerifier struct {
	rejected map[string]map[string]bool
}

type rankedDomainDownloadTester struct {
	mbps      map[string]float64
	calls     []string
	onMeasure func(address string)
}

func (*rankedDomainDownloadTester) DiscoverProbeURL(context.Context, string, string) (string, error) {
	return "https://manual.example/assets/probe.bin", nil
}

func (t *rankedDomainDownloadTester) Measure(_ context.Context, _, address, probeURL string) (acceleration.DownloadResult, error) {
	t.calls = append(t.calls, address)
	if t.onMeasure != nil {
		t.onMeasure(address)
	}
	return acceleration.DownloadResult{ProbeURL: probeURL, Downloaded: 1 << 20, Duration: time.Second, Mbps: t.mbps[address]}, nil
}

func (v *selectiveDomainVerifier) VerifyPreflight(_ context.Context, mappings []proxy.DomainMapping) error {
	for _, mapping := range mappings {
		for _, address := range mapping.Addresses {
			if v.rejected[mapping.Domain][address] {
				return errors.New("incompatible domain address")
			}
		}
	}
	return nil
}

func (*selectiveDomainVerifier) VerifyApplied(context.Context, []proxy.DomainMapping) error {
	return nil
}

type delayedRouteBackend struct {
	routes map[string]cfnetwork.RouteSpec
	delay  time.Duration
}

func (b *delayedRouteBackend) Replace(_ context.Context, route cfnetwork.RouteSpec) error {
	b.routes[route.Prefix] = route
	return nil
}

func (b *delayedRouteBackend) Delete(ctx context.Context, route cfnetwork.RouteSpec) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(b.delay):
	}
	if _, exists := b.routes[route.Prefix]; !exists {
		return cfnetwork.ErrRouteNotFound
	}
	delete(b.routes, route.Prefix)
	return nil
}

func (b *delayedRouteBackend) Get(_ context.Context, prefix string) (cfnetwork.RouteSpec, error) {
	route, exists := b.routes[prefix]
	if !exists {
		return cfnetwork.RouteSpec{}, cfnetwork.ErrRouteNotFound
	}
	return route, nil
}

func (b *delayedRouteBackend) Resolve(_ context.Context, target netip.Addr) (cfnetwork.ResolvedRoute, error) {
	for _, route := range b.routes {
		if netip.MustParsePrefix(route.Prefix).Contains(target) {
			return cfnetwork.ResolvedRoute{RouteSpec: route, SourceAddress: "192.0.2.10"}, nil
		}
	}
	return cfnetwork.ResolvedRoute{}, cfnetwork.ErrRouteNotFound
}

func (p *recordingPolicy) Capabilities() proxy.Capabilities {
	if p.capabilities == (proxy.Capabilities{}) {
		return proxy.Capabilities{IPv4: true}
	}
	return p.capabilities
}

func (p *recordingPolicy) Apply(_ context.Context, policy proxy.DirectPolicy) (proxy.ApplyResult, error) {
	p.policies = append(p.policies, policy)
	for _, mapping := range policy.DomainMappings {
		if mapping.Domain == p.rejectedDomain {
			return proxy.ApplyResult{}, fmt.Errorf("adapter verification: %w", &proxy.DomainVerificationError{Domain: mapping.Domain, Err: errors.New("connection evidence mismatch")})
		}
	}
	payload, _ := json.Marshal(policy)
	return proxy.ApplyResult{Receipts: []proxy.Receipt{{ID: "test", Adapter: "test", Changed: true, Payload: payload}}}, nil
}

func (p *recordingPolicy) Rollback(_ context.Context, applied proxy.ApplyResult) error {
	p.rollbacks = append(p.rollbacks, applied)
	return nil
}

func (p *candidateRejectingPolicy) Apply(ctx context.Context, policy proxy.DirectPolicy) (proxy.ApplyResult, error) {
	for _, mapping := range policy.DomainMappings {
		if p.rejectFirst && p.rejectedAddress == "" && len(mapping.Addresses) > 0 {
			p.rejectedAddress = mapping.Addresses[0]
		}
		if len(mapping.Addresses) > 0 && (p.rejectAll || mapping.Addresses[0] == p.rejectedAddress) {
			return proxy.ApplyResult{}, fmt.Errorf("candidate verification failed: %w", &proxy.DomainVerificationError{
				Domain: mapping.Domain, Address: mapping.Addresses[0], Kind: proxy.DomainVerificationCandidateUnreachable,
				Err: errors.New("candidate HTTPS connection unavailable"),
			})
		}
	}
	return p.recordingPolicy.Apply(ctx, policy)
}

func TestRunnerBenchmarkOnlyDoesNotChangeAppliedSelection(t *testing.T) {
	runner, stateStore := newTestRunner(t, nil)
	report, err := runner.Run(context.Background(), RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.IPv4Decision.HasSelection || report.PolicyApplied {
		t.Fatalf("unexpected report: %#v", report)
	}
	state := stateStore.Snapshot()
	if state.CurrentIPv4 != nil {
		t.Fatalf("benchmark-only run changed applied selection: %#v", state.CurrentIPv4)
	}
	if len(state.History) != 1 || state.Running {
		t.Fatalf("run was not finalized: %#v", state)
	}
}

func TestRunnerWaitsForPolicyRefreshAndKeepsDuplicateRunConflict(t *testing.T) {
	runner, stateStore := newTestRunner(t, &recordingPolicy{})
	blockingSource := &blockingRanges{
		snapshot: ranges.Snapshot{Version: 1, Source: "test", Hash: "test", IPv4: []string{"1.1.1.0/30"}},
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	runner.ranges = blockingSource
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.1", Family: 4, PolicyVerified: true}
		state.Nodes["1.1.1.1"] = store.NodeStats{Successes: 2, AverageScore: 99}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	refreshDone := make(chan error, 1)
	go func() { refreshDone <- runner.RefreshPolicy(context.Background()) }()
	waitForSignal(t, blockingSource.started, "policy refresh did not start")

	runDone := make(chan error, 1)
	go func() {
		_, err := runner.Run(context.Background(), RunOptions{}, nil)
		runDone <- err
	}()
	waitForPendingRun(t, runner)

	select {
	case err := <-runDone:
		t.Fatalf("manual run returned before policy refresh completed: %v", err)
	default:
	}
	if err := runner.RefreshPolicy(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("new maintenance did not yield to the pending manual run: %v", err)
	}
	if _, err := runner.Run(context.Background(), RunOptions{}, nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("duplicate manual run did not retain conflict semantics: %v", err)
	}

	close(blockingSource.release)
	if err := waitForResult(t, refreshDone, "policy refresh did not finish"); err != nil {
		t.Fatal(err)
	}
	if err := waitForResult(t, runDone, "manual run did not start after policy refresh"); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerCancelsWhileWaitingForPolicyRefresh(t *testing.T) {
	runner, stateStore := newTestRunner(t, &recordingPolicy{})
	blockingSource := &blockingRanges{
		snapshot: ranges.Snapshot{Version: 1, Source: "test", Hash: "test", IPv4: []string{"1.1.1.0/30"}},
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	runner.ranges = blockingSource
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.1", Family: 4, PolicyVerified: true}
		state.Nodes["1.1.1.1"] = store.NodeStats{Successes: 2, AverageScore: 99}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	refreshDone := make(chan error, 1)
	go func() { refreshDone <- runner.RefreshPolicy(context.Background()) }()
	waitForSignal(t, blockingSource.started, "policy refresh did not start")

	runContext, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		_, err := runner.Run(runContext, RunOptions{}, nil)
		runDone <- err
	}()
	waitForPendingRun(t, runner)
	cancel()
	if err := waitForResult(t, runDone, "cancelled manual run did not return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("manual run returned an unexpected cancellation error: %v", err)
	}
	if pending := runner.pendingRuns.Load(); pending != 0 {
		t.Fatalf("cancelled manual run left a pending reservation: %d", pending)
	}

	close(blockingSource.release)
	if err := waitForResult(t, refreshDone, "policy refresh did not finish"); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeCancellationClearsLastTaskError(t *testing.T) {
	stateStore, err := store.Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Update(func(state *store.State) error {
		state.Running = true
		state.LastError = "previous failure"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	runner := &Runner{store: stateStore}
	if err := runner.finalize(RunReport{ID: "cancelled-run", StartedAt: now.Add(-time.Second), FinishedAt: now}, fmt.Errorf("query route: %w", context.Canceled)); err != nil {
		t.Fatal(err)
	}
	state := stateStore.Snapshot()
	if state.Running || state.LastError != "" {
		t.Fatalf("cancelled run remained failed: %#v", state)
	}
	if len(state.History) != 1 || state.History[0].Error != "optimization was cancelled" {
		t.Fatalf("cancelled run audit summary is missing: %#v", state.History)
	}
}

func TestRunnerAppliesAndPersistsVerifiedSelection(t *testing.T) {
	policy := &recordingPolicy{}
	runner, stateStore := newTestRunner(t, policy)
	var events []Event
	report, err := runner.Run(context.Background(), RunOptions{ApplyPolicy: true}, func(event Event) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	state := stateStore.Snapshot()
	if !report.PolicyApplied || state.CurrentIPv4 == nil || !state.CurrentIPv4.PolicyVerified || state.Policy == nil {
		t.Fatalf("verified selection was not persisted: report=%#v state=%#v", report, state)
	}
	if len(policy.policies) != 1 || len(policy.policies[0].IPv4CIDRs) != 1 {
		t.Fatalf("unexpected applied policy: %#v", policy.policies)
	}
	if !slices.ContainsFunc(events, func(event Event) bool { return event.Stage == "policy" }) {
		t.Fatalf("policy application stage was not emitted: %#v", events)
	}
}

func TestRunnerRejectsPolicyApplicationWithTypedNilCoordinator(t *testing.T) {
	var coordinator *proxy.Coordinator
	runner, _ := newTestRunner(t, coordinator)
	_, err := runner.Run(context.Background(), RunOptions{ApplyPolicy: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "no adapter is configured") {
		t.Fatalf("typed-nil coordinator did not return the expected policy error: %v", err)
	}
}

func TestAllocateDomainMappingsUsesManualConfigurationAndRankingOrder(t *testing.T) {
	policyApplier := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}}
	runner, _ := newTestRunner(t, policyApplier)
	runner.config.Acceleration.ManualDomains = []string{"priority.example", "second.example", "unassigned.example"}
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{
		"priority.example": {"1.1.1.1": true},
	}}
	snapshot := ranges.Snapshot{IPv4: []string{"1.1.1.0/24"}}
	results := []benchmark.Result{
		{IP: netip.MustParseAddr("1.1.1.1"), Qualified: true, TLSVerified: true, Score: 99},
		{IP: netip.MustParseAddr("1.1.1.2"), Qualified: true, TLSVerified: true, Score: 98},
		{IP: netip.MustParseAddr("1.1.1.3"), Qualified: true, TLSVerified: true, Score: 97},
	}

	mappings, allocations, warnings, err := runner.allocateDomainMappings(context.Background(), snapshot, results, store.State{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || len(mappings) != 2 || len(allocations) != 3 {
		t.Fatalf("unexpected allocation result: mappings=%#v allocations=%#v warnings=%#v", mappings, allocations, warnings)
	}
	if mappings[0].Domain != "priority.example" || mappings[0].Addresses[0] != "1.1.1.2" {
		t.Fatalf("first manual domain did not consume ranked candidates in order: %#v", mappings)
	}
	if mappings[1].Domain != "second.example" || mappings[1].Addresses[0] != "1.1.1.3" {
		t.Fatalf("second manual domain did not receive the next address: %#v", mappings)
	}
	if allocations[0].AssignedAddress != "1.1.1.2" || !allocations[0].CloudflareVerified || !allocations[0].PreflightVerified || allocations[2].Error == "" {
		t.Fatalf("structured domain allocation evidence is incomplete: %#v", allocations)
	}
}

func TestAllocateDomainMappingsUsesFirstCandidateMeetingManualDownloadThreshold(t *testing.T) {
	policyApplier := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}}
	runner, _ := newTestRunner(t, policyApplier)
	runner.config.Acceleration.ManualDomains = []string{"manual.example"}
	runner.config.Acceleration.ManualDownloadTest = true
	runner.config.Acceleration.ManualDownloadMinMbps = 20
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}
	downloadTester := &rankedDomainDownloadTester{mbps: map[string]float64{"1.1.1.1": 12.5, "1.1.1.2": 24.75, "1.1.1.3": 80}}
	runner.domainDownload = downloadTester
	results := []benchmark.Result{
		{IP: netip.MustParseAddr("1.1.1.1"), Qualified: true, Score: 99},
		{IP: netip.MustParseAddr("1.1.1.2"), Qualified: true, Score: 98},
		{IP: netip.MustParseAddr("1.1.1.3"), Qualified: true, Score: 97},
	}

	mappings, allocations, warnings, err := runner.allocateDomainMappings(context.Background(), ranges.Snapshot{IPv4: []string{"1.1.1.0/24"}}, results, store.State{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(mappings) != 1 || mappings[0].Addresses[0] != "1.1.1.2" {
		t.Fatalf("unexpected threshold allocation: mappings=%#v warnings=%#v", mappings, warnings)
	}
	if len(allocations) != 1 || !allocations[0].DownloadVerified || allocations[0].DownloadMbps != 24.75 {
		t.Fatalf("download verification evidence is incomplete: %#v", allocations)
	}
	if !slices.Equal(downloadTester.calls, []string{"1.1.1.1", "1.1.1.2"}) {
		t.Fatalf("candidate testing did not stop at the first passing address: %#v", downloadTester.calls)
	}
}

func TestAllocateDomainMappingsAppliesTemporaryRouteDuringManualDownload(t *testing.T) {
	policyApplier := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}}
	runner, _ := newTestRunner(t, policyApplier)
	runner.config.Acceleration.ManualDomains = []string{"manual.example"}
	runner.config.Acceleration.ManualDownloadTest = true
	runner.config.Acceleration.ManualDownloadMinMbps = 20
	runner.config.Network.ManageRoutes = true
	runner.physicalPath = cfnetwork.PhysicalPath{Interface: "eth0", InterfaceIndex: 2, GatewayIPv4: "192.0.2.1"}
	backend := &delayedRouteBackend{routes: map[string]cfnetwork.RouteSpec{}}
	controller, err := cfnetwork.NewRouteController(t.TempDir(), backend, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	runner.routes = controller
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}
	runner.domainDownload = &rankedDomainDownloadTester{
		mbps: map[string]float64{"1.1.1.1": 25},
		onMeasure: func(address string) {
			route, exists := backend.routes[address+"/32"]
			if !exists || route.Gateway != "192.0.2.1" || route.Interface != "eth0" || route.InterfaceIndex != 2 {
				t.Fatalf("manual download did not use the expected temporary physical route: %#v", backend.routes)
			}
		},
	}
	results := []benchmark.Result{{IP: netip.MustParseAddr("1.1.1.1"), Qualified: true, Score: 99}}

	mappings, allocations, warnings, err := runner.allocateDomainMappings(context.Background(), ranges.Snapshot{IPv4: []string{"1.1.1.0/24"}}, results, store.State{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(mappings) != 1 || len(allocations) != 1 || !allocations[0].DownloadVerified {
		t.Fatalf("unexpected routed download allocation: mappings=%#v allocations=%#v warnings=%#v", mappings, allocations, warnings)
	}
	if len(backend.routes) != 0 {
		t.Fatalf("temporary manual download route was not rolled back: %#v", backend.routes)
	}
}

func TestAllocateDomainMappingsGivesRemainingPoolToAutomaticDomains(t *testing.T) {
	policyApplier := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}}
	runner, stateStore := newTestRunner(t, policyApplier)
	runner.config.Acceleration.Enabled = true
	runner.config.Acceleration.AutoDiscover = true
	runner.config.Acceleration.AutoApply = true
	runner.config.Acceleration.ManualDomains = []string{"first.example", "second.example"}
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}
	if err := stateStore.Update(func(state *store.State) error {
		for _, domain := range []string{"auto-b.example", "auto-a.example"} {
			state.DiscoveredDomains[domain] = store.DomainDiscovery{
				Domain: domain, CloudflareVerified: true, PreflightVerified: true, Active: true,
				LastResolvedAddresses: []string{"1.1.1.1"},
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	results := []benchmark.Result{
		{IP: netip.MustParseAddr("1.1.1.1"), Qualified: true, Score: 99},
		{IP: netip.MustParseAddr("1.1.1.2"), Qualified: true, Score: 98},
		{IP: netip.MustParseAddr("1.1.1.3"), Qualified: true, Score: 97},
		{IP: netip.MustParseAddr("1.1.1.4"), Qualified: true, Score: 96},
	}

	mappings, _, warnings, err := runner.allocateDomainMappings(context.Background(), ranges.Snapshot{IPv4: []string{"1.1.1.0/24"}}, results, stateStore.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(mappings) != 4 {
		t.Fatalf("unexpected combined allocation: mappings=%#v warnings=%#v", mappings, warnings)
	}
	wantDomains := []string{"first.example", "second.example", "auto-a.example", "auto-b.example"}
	for index, mapping := range mappings {
		if mapping.Domain != wantDomains[index] || mapping.Addresses[0] != results[index].IP.String() {
			t.Fatalf("combined pool allocation order is incorrect: %#v", mappings)
		}
	}
}

func TestAllocateDomainMappingsIgnoresAutomaticDomainsWithoutAllSwitches(t *testing.T) {
	policyApplier := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}}
	runner, stateStore := newTestRunner(t, policyApplier)
	runner.config.Acceleration.Enabled = true
	runner.config.Acceleration.AutoDiscover = false
	runner.config.Acceleration.AutoApply = true
	runner.config.Acceleration.ManualDomains = []string{"manual.example"}
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}
	if err := stateStore.Update(func(state *store.State) error {
		state.DiscoveredDomains["auto.example"] = store.DomainDiscovery{
			Domain: "auto.example", CloudflareVerified: true, PreflightVerified: true, Active: true,
			LastResolvedAddresses: []string{"1.1.1.1"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	results := []benchmark.Result{
		{IP: netip.MustParseAddr("1.1.1.1"), Qualified: true, Score: 99},
		{IP: netip.MustParseAddr("1.1.1.2"), Qualified: true, Score: 98},
	}

	mappings, _, warnings, err := runner.allocateDomainMappings(context.Background(), ranges.Snapshot{IPv4: []string{"1.1.1.0/24"}}, results, stateStore.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(mappings) != 1 || mappings[0].Domain != "manual.example" || mappings[0].Addresses[0] != "1.1.1.1" {
		t.Fatalf("automatic domain consumed the pool without all switches: mappings=%#v warnings=%#v", mappings, warnings)
	}
}

func TestRunPersistsManualDomainAllocationFailure(t *testing.T) {
	policy := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}}
	runner, stateStore := newTestRunner(t, policy)
	runner.config.Acceleration.Enabled = true
	runner.config.Acceleration.ManualDomains = []string{"ani.momoc.top"}
	runner.domainResolver = staticDomainResolver{err: errors.New("physical DNS timeout")}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}

	report, err := runner.Run(context.Background(), RunOptions{ApplyPolicy: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "physical DNS timeout") {
		t.Fatalf("manual domain failure should stop policy application: %v", err)
	}
	if report.PolicyApplied || len(policy.policies) != 0 || len(report.DomainAllocations) != 1 || report.DomainAllocations[0].AssignedAddress != "" || !strings.Contains(report.DomainAllocations[0].Error, "physical DNS timeout") {
		t.Fatalf("manual domain failure was not exposed in run report: %#v", report)
	}
	record := stateStore.Snapshot().DiscoveredDomains["ani.momoc.top"]
	if record.Source != "manual" || record.Active || !strings.Contains(record.LastError, "physical DNS timeout") {
		t.Fatalf("manual domain failure was not persisted: %#v", record)
	}
	if history := stateStore.Snapshot().History; len(history) != 1 || !strings.Contains(history[0].Error, "ani.momoc.top") {
		t.Fatalf("manual domain failure was not marked partial in history: %#v", history)
	}
}

func TestRunKeepsPreviousPolicyWhenRankedPoolHasNoQualifiedAddress(t *testing.T) {
	policy := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}}
	runner, stateStore := newTestRunner(t, policy)
	runner.benchmark = unqualifiedBenchmark{}
	runner.config.Acceleration.Enabled = true
	runner.config.Acceleration.ManualDomains = []string{"ani.momoc.top"}
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}
	previousReceipts, err := json.Marshal(proxy.ApplyResult{Receipts: []proxy.Receipt{{ID: "previous", Adapter: "test", Changed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	previousMapping := store.DomainMappingSnapshot{Domain: "ani.momoc.top", Addresses: []string{"1.1.1.3"}}
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.3", Family: 4, Score: 98, PolicyVerified: true}
		state.Policy = &store.PolicySnapshot{
			IPv4CIDRs: []string{"1.1.1.3/32"}, DomainMappings: []store.DomainMappingSnapshot{previousMapping}, Receipts: previousReceipts,
		}
		state.DiscoveredDomains["ani.momoc.top"] = store.DomainDiscovery{
			Domain: "ani.momoc.top", Source: "manual", CloudflareVerified: true, PreflightVerified: true, Active: true,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	report, err := runner.Run(context.Background(), RunOptions{ApplyPolicy: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "ranked address pool is exhausted") {
		t.Fatalf("empty ranked pool should fail with explicit context: %v", err)
	}
	state := stateStore.Snapshot()
	if report.PolicyApplied || len(policy.policies) != 0 || state.Policy == nil || !reflect.DeepEqual(state.Policy.DomainMappings, []store.DomainMappingSnapshot{previousMapping}) {
		t.Fatalf("empty ranked pool replaced the previous policy: report=%#v policy=%#v applied=%#v", report, state.Policy, policy.policies)
	}
	record := state.DiscoveredDomains["ani.momoc.top"]
	if !record.Active || !record.CloudflareVerified || !record.PreflightVerified || !strings.Contains(record.LastError, "ranked address pool is exhausted") {
		t.Fatalf("failed refresh did not retain verified domain state: %#v", record)
	}
}

func TestRunKeepsPreviousPolicyWhenAllManualDownloadCandidatesAreBelowThreshold(t *testing.T) {
	policy := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}}
	runner, stateStore := newTestRunner(t, policy)
	runner.config.Acceleration.Enabled = true
	runner.config.Acceleration.ManualDomains = []string{"ani.momoc.top"}
	runner.config.Acceleration.ManualDownloadTest = true
	runner.config.Acceleration.ManualDownloadMinMbps = 20
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}
	runner.domainDownload = &rankedDomainDownloadTester{mbps: map[string]float64{
		"1.1.1.1": 8.5,
		"1.1.1.2": 12.25,
	}}
	previousReceipts, err := json.Marshal(proxy.ApplyResult{Receipts: []proxy.Receipt{{ID: "previous", Adapter: "test", Changed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	previousMapping := store.DomainMappingSnapshot{Domain: "ani.momoc.top", Addresses: []string{"1.1.1.3"}}
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.3", Family: 4, Score: 98, PolicyVerified: true}
		state.Policy = &store.PolicySnapshot{
			IPv4CIDRs: []string{"1.1.1.3/32"}, DomainMappings: []store.DomainMappingSnapshot{previousMapping}, Receipts: previousReceipts,
		}
		state.DiscoveredDomains["ani.momoc.top"] = store.DomainDiscovery{
			Domain: "ani.momoc.top", Source: "manual", CloudflareVerified: true, PreflightVerified: true, Active: true,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	report, err := runner.Run(context.Background(), RunOptions{ApplyPolicy: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "below configured 20.00 Mbps") {
		t.Fatalf("download threshold exhaustion should fail with measured-speed context: %v", err)
	}
	state := stateStore.Snapshot()
	if report.PolicyApplied || len(policy.policies) != 0 || state.Policy == nil || !reflect.DeepEqual(state.Policy.DomainMappings, []store.DomainMappingSnapshot{previousMapping}) {
		t.Fatalf("download threshold exhaustion replaced the previous policy: report=%#v policy=%#v applied=%#v", report, state.Policy, policy.policies)
	}
	record := state.DiscoveredDomains["ani.momoc.top"]
	if !record.Active || !record.CloudflareVerified || !record.PreflightVerified || !strings.Contains(record.LastError, "below configured 20.00 Mbps") {
		t.Fatalf("failed download refresh did not retain verified domain state: %#v", record)
	}
}

func TestRunRetriesNextCandidateAfterApplicationVerificationFailure(t *testing.T) {
	policy := &candidateRejectingPolicy{
		recordingPolicy: recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}},
		rejectFirst:     true,
	}
	runner, stateStore := newTestRunner(t, policy)
	runner.config.Acceleration.Enabled = true
	runner.config.Acceleration.ManualDomains = []string{"ani.momoc.top"}
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}

	report, err := runner.Run(context.Background(), RunOptions{ApplyPolicy: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.PolicyApplied || len(report.DomainAllocations) != 1 || report.DomainAllocations[0].AssignedAddress == "" || report.DomainAllocations[0].AssignedAddress == policy.rejectedAddress {
		t.Fatalf("application failure did not advance to the next candidate: %#v", report.DomainAllocations)
	}
	assignedAddress := report.DomainAllocations[0].AssignedAddress
	state := stateStore.Snapshot()
	if state.Policy == nil || len(state.Policy.DomainMappings) != 1 || state.Policy.DomainMappings[0].Addresses[0] != assignedAddress {
		t.Fatalf("successful fallback mapping was not persisted: %#v", state.Policy)
	}
	failedNode := state.Nodes[policy.rejectedAddress]
	if failedNode.CooldownUntil.IsZero() || failedNode.FailureStreak < 1 {
		t.Fatalf("failed candidate %s was not persisted in cooldown: %#v", policy.rejectedAddress, failedNode)
	}
}

func TestRunKeepsPreviousPolicyWhenManualCandidatesAreExhausted(t *testing.T) {
	policy := &candidateRejectingPolicy{
		recordingPolicy: recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}},
		rejectAll:       true,
	}
	runner, stateStore := newTestRunner(t, policy)
	runner.config.Acceleration.Enabled = true
	runner.config.Acceleration.ManualDomains = []string{"ani.momoc.top"}
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}
	previousReceipts, err := json.Marshal(proxy.ApplyResult{Receipts: []proxy.Receipt{{ID: "previous", Adapter: "test", Changed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.3", Family: 4, PolicyVerified: true}
		state.Policy = &store.PolicySnapshot{
			IPv4CIDRs:      []string{"1.1.1.3/32"},
			DomainMappings: []store.DomainMappingSnapshot{{Domain: "ani.momoc.top", Addresses: []string{"1.1.1.3"}}},
			Receipts:       previousReceipts,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err = runner.Run(context.Background(), RunOptions{ApplyPolicy: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "ani.momoc.top") {
		t.Fatalf("exhausted manual candidates should fail with domain context: %v", err)
	}
	state := stateStore.Snapshot()
	if state.Policy == nil || !reflect.DeepEqual(state.Policy.DomainMappings, []store.DomainMappingSnapshot{{Domain: "ani.momoc.top", Addresses: []string{"1.1.1.3"}}}) {
		t.Fatalf("previous policy was not preserved after fallback exhaustion: %#v", state.Policy)
	}
}

func TestRunRollsBackBenchmarkGuardBeforeFinalPolicy(t *testing.T) {
	policy := &guardedRecordingPolicy{recordingPolicy: recordingPolicy{capabilities: proxy.Capabilities{IPv4: true}}}
	runner, _ := newTestRunner(t, policy)
	report, err := runner.Run(context.Background(), RunOptions{ApplyPolicy: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(policy.events, ",") != "guard_begin,guard_end,policy_apply" {
		t.Fatalf("unexpected benchmark guard lifecycle: %#v", policy.events)
	}
	if len(report.BenchmarkPath) != 1 || !report.BenchmarkPath[0].DirectVerified || policy.guardActive {
		t.Fatalf("benchmark guard evidence or cleanup is incomplete: report=%#v active=%v", report.BenchmarkPath, policy.guardActive)
	}
}

func TestPolicyForDecisionsMapsAllocatedManualAndAutomaticDomains(t *testing.T) {
	policyApplier := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, IPv6: true, Domains: true, DomainMappings: true}}
	runner, stateStore := newTestRunner(t, policyApplier)
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.1", Family: 4, PolicyVerified: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	report := RunReport{domainAllocationCompleted: true, domainMappings: []proxy.DomainMapping{
		{Domain: "manual.example", Addresses: []string{"1.1.1.2"}},
		{Domain: "auto.example", Addresses: []string{"1.1.1.3"}},
	}}
	policy, err := runner.policyForDecisions(stateStore.Snapshot(), report, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.DomainMappings) != 2 || policy.DomainMappings[0].Domain != "auto.example" || policy.DomainMappings[0].Addresses[0] != "1.1.1.3" || policy.DomainMappings[1].Domain != "manual.example" || policy.DomainMappings[1].Addresses[0] != "1.1.1.2" {
		t.Fatalf("allocated domain mappings were not applied: %#v", policy.DomainMappings)
	}
	wantIPv4 := map[string]bool{"1.1.1.1/32": true, "1.1.1.2/32": true, "1.1.1.3/32": true}
	for _, prefix := range policy.IPv4CIDRs {
		delete(wantIPv4, prefix)
	}
	if len(wantIPv4) != 0 || len(policy.IPv6CIDRs) != 0 {
		t.Fatalf("allocated addresses were not converted to host routes: %#v %#v", policy.IPv4CIDRs, policy.IPv6CIDRs)
	}
}

func TestRunnerAccumulatesReceiptsAndCleansManagedPolicy(t *testing.T) {
	policy := &recordingPolicy{}
	runner, stateStore := newTestRunner(t, policy)
	for run := 0; run < 2; run++ {
		if _, err := runner.Run(context.Background(), RunOptions{ApplyPolicy: true}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var applied proxy.ApplyResult
	if err := json.Unmarshal(stateStore.Snapshot().Policy.Receipts, &applied); err != nil {
		t.Fatal(err)
	}
	if len(applied.Receipts) != 2 {
		t.Fatalf("expected cumulative cleanup receipts, got %#v", applied.Receipts)
	}
	if err := runner.CleanupManagedPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := stateStore.Snapshot()
	if state.Policy != nil || state.CurrentIPv4 != nil || len(policy.rollbacks) != 1 || len(policy.rollbacks[0].Receipts) != 2 {
		t.Fatalf("unexpected cleanup result: state=%#v rollbacks=%#v", state, policy.rollbacks)
	}
	if err := runner.CleanupManagedPolicy(context.Background()); err != nil {
		t.Fatalf("cleanup should be idempotent: %v", err)
	}
}

func TestRecoverPendingPolicyRollsBackBeforeClearingJournal(t *testing.T) {
	policy := &recordingPolicy{}
	_, stateStore := newTestRunner(t, policy)
	receipts, err := json.Marshal(proxy.ApplyResult{Receipts: []proxy.Receipt{{ID: "pending", Adapter: "test", Changed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Update(func(state *store.State) error {
		state.PendingPolicy = store.NewPolicyTransaction(time.Now(), json.RawMessage(`{}`), receipts)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := RecoverPendingPolicy(context.Background(), stateStore, policy); err != nil {
		t.Fatal(err)
	}
	if stateStore.Snapshot().PendingPolicy != nil || len(policy.rollbacks) != 1 || policy.rollbacks[0].Receipts[0].ID != "pending" {
		t.Fatalf("pending policy was not recovered: state=%#v rollbacks=%#v", stateStore.Snapshot(), policy.rollbacks)
	}
}

func TestRunnerClearsUnsafeLegacyMappingsBeforeFailedReplacement(t *testing.T) {
	policy := &recordingPolicy{
		capabilities:   proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true},
		rejectedDomain: "second.example",
	}
	runner, stateStore := newTestRunner(t, policy)
	runner.config.Acceleration.Enabled = true
	runner.config.Acceleration.ManualDomains = []string{"first.example", "second.example"}
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}
	storedReceipts, err := json.Marshal(proxy.ApplyResult{Receipts: []proxy.Receipt{{ID: "legacy", Adapter: "test", Changed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.1", Family: 4, PolicyVerified: true}
		state.Policy = &store.PolicySnapshot{
			DomainMappings: []store.DomainMappingSnapshot{
				{Domain: "first.example", Addresses: []string{"1.1.1.1"}},
				{Domain: "second.example", Addresses: []string{"1.1.1.1"}},
			},
			Receipts: storedReceipts,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := runner.Run(context.Background(), RunOptions{ApplyPolicy: true}, nil); err == nil {
		t.Fatal("replacement policy must fail its simulated connection verification")
	}
	state := stateStore.Snapshot()
	if state.Policy == nil || len(state.Policy.DomainMappings) != 0 || state.CurrentIPv4 == nil {
		t.Fatalf("replacement failure did not preserve the safe DNS policy: %#v", state.Policy)
	}
	if len(policy.policies) < 2 || len(policy.policies[0].DomainMappings) != 0 {
		t.Fatalf("safe policy was not applied before the replacement attempt: %#v", policy.policies)
	}
	var currentReceipts proxy.ApplyResult
	if err := json.Unmarshal(state.Policy.Receipts, &currentReceipts); err != nil {
		t.Fatal(err)
	}
	if len(currentReceipts.Receipts) != 1 || currentReceipts.Receipts[0].ID == "legacy" {
		t.Fatalf("broken legacy receipt chain was retained: %#v", currentReceipts)
	}
}

func TestRefreshPolicyRollsBackOnlyNewReceiptsWhenPreviousReceiptsAreInvalid(t *testing.T) {
	policy := &recordingPolicy{}
	runner, stateStore := newTestRunner(t, policy)
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.1", Family: 4, PolicyVerified: true}
		state.Policy = &store.PolicySnapshot{Receipts: json.RawMessage(`[]`)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := runner.RefreshPolicy(context.Background()); err == nil {
		t.Fatal("expected invalid previous receipts to fail refresh")
	}
	if len(policy.rollbacks) != 1 || len(policy.rollbacks[0].Receipts) != 1 {
		t.Fatalf("refresh should roll back only its new receipt: %#v", policy.rollbacks)
	}
	if got := stateStore.Snapshot().Policy.Receipts; string(got) != `[]` {
		t.Fatalf("failed refresh changed stored receipts: %s", got)
	}
}

func TestUpdateAccelerationDomainsRemovesStoredManualMapping(t *testing.T) {
	policy := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}}
	runner, stateStore := newTestRunner(t, policy)
	runner.config.Acceleration.ManualDomains = []string{"ani.momoc.top"}
	receipts, err := json.Marshal(proxy.ApplyResult{Receipts: []proxy.Receipt{{ID: "previous", Adapter: "test", Changed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.1", Family: 4, PolicyVerified: true}
		state.Policy = &store.PolicySnapshot{
			DomainMappings: []store.DomainMappingSnapshot{{Domain: "ani.momoc.top", Addresses: []string{"1.1.1.1"}}},
			Receipts:       receipts,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	refreshed, err := runner.UpdateAccelerationDomains(context.Background(), []string{}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed {
		t.Fatal("active policy was not reported as refreshed")
	}
	if len(runner.config.Acceleration.ManualDomains) != 0 {
		t.Fatalf("manual domains were not updated: %#v", runner.config.Acceleration.ManualDomains)
	}
	state := stateStore.Snapshot()
	if state.Policy == nil || len(state.Policy.DomainMappings) != 0 {
		t.Fatalf("stored manual mapping was not removed: %#v", state.Policy)
	}
	if len(policy.policies) != 1 || len(policy.policies[0].DomainMappings) != 0 {
		t.Fatalf("replacement policy retained the removed domain: %#v", policy.policies)
	}
}

func TestUpdateAccelerationDomainsWithoutAdapterKeepsStalePolicyForCleanup(t *testing.T) {
	runner, stateStore := newTestRunner(t, nil)
	runner.config.Acceleration.ManualDomains = []string{"old.example"}
	if err := stateStore.Update(func(state *store.State) error {
		state.Policy = &store.PolicySnapshot{Domains: []string{"old.example"}, Receipts: json.RawMessage(`{"receipts":[]}`)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	refreshed, err := runner.UpdateAccelerationDomains(context.Background(), []string{"new.example"}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed {
		t.Fatal("domain update without an adapter must not report a policy refresh")
	}
	if !slices.Equal(runner.config.Acceleration.ManualDomains, []string{"new.example"}) {
		t.Fatalf("manual domains were not updated: %#v", runner.config.Acceleration.ManualDomains)
	}
	policy := stateStore.Snapshot().Policy
	if policy == nil || !slices.Equal(policy.Domains, []string{"old.example"}) {
		t.Fatalf("stale policy receipts were discarded before cleanup: %#v", policy)
	}
}

func TestClearDiscoveredDomainsRemovesAutomaticAccelerationAndPreservesManualDomain(t *testing.T) {
	policy := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}}
	runner, stateStore := newTestRunner(t, policy)
	runner.config.Acceleration.Enabled = true
	runner.config.Acceleration.AutoDiscover = true
	runner.config.Acceleration.AutoApply = true
	runner.config.Acceleration.ManualDomains = []string{"manual.example"}
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}
	receipts, err := json.Marshal(proxy.ApplyResult{Receipts: []proxy.Receipt{{ID: "previous", Adapter: "test", Changed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.1", Family: 4, PolicyVerified: true}
		state.Nodes["1.1.1.1"] = store.NodeStats{Successes: 2, AverageScore: 99}
		state.Nodes["1.1.1.2"] = store.NodeStats{Successes: 2, AverageScore: 98}
		state.DiscoveredDomains["manual.example"] = store.DomainDiscovery{Domain: "manual.example", Source: "mihomo", Active: true, CloudflareVerified: true, PreflightVerified: true, LastResolvedAddresses: []string{"1.1.1.1"}}
		state.DiscoveredDomains["auto.example"] = store.DomainDiscovery{Domain: "auto.example", Source: "mihomo", Active: true, CloudflareVerified: true, PreflightVerified: true, LastResolvedAddresses: []string{"1.1.1.1"}}
		state.Policy = &store.PolicySnapshot{
			DomainMappings: []store.DomainMappingSnapshot{
				{Domain: "manual.example", Addresses: []string{"1.1.1.1"}},
				{Domain: "auto.example", Addresses: []string{"1.1.1.2"}},
			},
			Receipts: receipts,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	result, err := runner.ClearDiscoveredDomains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Cleared != 1 || result.AccelerationsRemoved != 1 || !result.PolicyRefreshed {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	state := stateStore.Snapshot()
	manualRecord, manualRecordExists := state.DiscoveredDomains["manual.example"]
	if len(state.DiscoveredDomains) != 1 || !manualRecordExists || manualRecord.Source != "manual" || !manualRecord.Active {
		t.Fatalf("manual domain evidence was not preserved: %#v", state.DiscoveredDomains)
	}
	if state.Policy == nil || len(state.Policy.DomainMappings) != 1 || state.Policy.DomainMappings[0].Domain != "manual.example" {
		t.Fatalf("manual acceleration was not preserved: %#v", state.Policy)
	}
	if len(policy.policies) != 1 || len(policy.policies[0].DomainMappings) != 1 || policy.policies[0].DomainMappings[0].Domain != "manual.example" {
		t.Fatalf("replacement policy did not preserve only the manual domain: %#v", policy.policies)
	}
}

func TestClearDiscoveredDomainsFailurePreservesRecordsAndStoredPolicy(t *testing.T) {
	policy := &recordingPolicy{capabilities: proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true}, rejectedDomain: "manual.example"}
	runner, stateStore := newTestRunner(t, policy)
	runner.config.Acceleration.ManualDomains = []string{"manual.example"}
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}
	previousMappings := []store.DomainMappingSnapshot{
		{Domain: "manual.example", Addresses: []string{"1.1.1.1"}},
		{Domain: "auto.example", Addresses: []string{"1.1.1.2"}},
	}
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.1", Family: 4, PolicyVerified: true}
		state.Nodes["1.1.1.1"] = store.NodeStats{Successes: 2, AverageScore: 99}
		state.DiscoveredDomains["auto.example"] = store.DomainDiscovery{Domain: "auto.example", Source: "mihomo", Active: true}
		state.Policy = &store.PolicySnapshot{DomainMappings: previousMappings, Receipts: json.RawMessage(`{"receipts":[]}`)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := runner.ClearDiscoveredDomains(context.Background()); err == nil {
		t.Fatal("expected replacement policy failure")
	}
	state := stateStore.Snapshot()
	if len(state.DiscoveredDomains) != 1 || state.Policy == nil || !reflect.DeepEqual(state.Policy.DomainMappings, previousMappings) {
		t.Fatalf("failed cleanup changed durable state: %#v", state)
	}
}

func TestRollbackRoutesUsesIndependentCleanupTimeouts(t *testing.T) {
	backend := &delayedRouteBackend{routes: map[string]cfnetwork.RouteSpec{}, delay: 30 * time.Millisecond}
	controller, err := cfnetwork.NewRouteController(t.TempDir(), backend, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var transactionIDs []string
	for _, prefix := range []string{"1.1.1.1/32", "1.1.1.2/32", "1.1.1.3/32", "1.1.1.4/32", "1.1.1.5/32"} {
		route := cfnetwork.RouteSpec{Prefix: prefix, Gateway: "192.0.2.1", Interface: "eth0", InterfaceIndex: 2, Metric: 5}
		plan, planErr := controller.Plan(context.Background(), route, true)
		if planErr != nil {
			t.Fatal(planErr)
		}
		transaction, applyErr := controller.Apply(context.Background(), plan)
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		transactionIDs = append(transactionIDs, transaction.ID)
	}
	cfg := config.Default()
	cfg.Network.CommandTimeout = config.Duration(time.Millisecond)
	runner := &Runner{config: cfg, routes: controller}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.rollbackRoutes(canceledContext, transactionIDs); err != nil {
		t.Fatal(err)
	}
	if len(backend.routes) != 0 {
		t.Fatalf("temporary routes remain after cleanup: %#v", backend.routes)
	}
	for _, transaction := range controller.Transactions() {
		if transaction.State != "rolled_back" {
			t.Fatalf("unexpected transaction state: %#v", transaction)
		}
	}
	if !errors.Is(canceledContext.Err(), context.Canceled) {
		t.Fatal("test context should remain canceled")
	}
}

func TestObsoletePolicyPrefixesReturnsOnlyRemovedHostRoutes(t *testing.T) {
	previous := &store.PolicySnapshot{
		IPv4CIDRs: []string{"1.1.1.1/32", "1.1.1.2/32"},
		IPv6CIDRs: []string{"2606:4700::1/128"},
	}
	next := proxy.DirectPolicy{IPv4CIDRs: []string{"1.1.1.2/32", "1.1.1.3/32"}}
	want := []string{"1.1.1.1/32", "2606:4700::1/128"}
	if got := obsoletePolicyPrefixes(previous, next); !slices.Equal(got, want) {
		t.Fatalf("obsoletePolicyPrefixes() = %#v, want %#v", got, want)
	}
}

func TestRemoveObsoletePolicyRoutesHandlesDomainAllocationAddressChange(t *testing.T) {
	oldRoute := cfnetwork.RouteSpec{Prefix: "104.18.1.10/32", Gateway: "192.0.2.1", Interface: "eth0", InterfaceIndex: 2, Metric: 5}
	backend := &delayedRouteBackend{routes: map[string]cfnetwork.RouteSpec{oldRoute.Prefix: oldRoute}}
	controller, err := cfnetwork.NewRouteController(t.TempDir(), backend, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Network.ManageRoutes = true
	runner := &Runner{
		config: cfg, routes: controller,
		physicalPath: cfnetwork.PhysicalPath{Interface: "eth0", InterfaceIndex: 2, GatewayIPv4: "192.0.2.1"},
	}
	previous := &store.PolicySnapshot{IPv4CIDRs: []string{oldRoute.Prefix}}
	next := proxy.DirectPolicy{IPv4CIDRs: []string{"104.18.1.12/32"}}

	transactionIDs, err := runner.removeObsoletePolicyRoutes(context.Background(), previous, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(transactionIDs) != 1 {
		t.Fatalf("obsolete domain allocation route did not create a removal transaction: %#v", transactionIDs)
	}
	if _, exists := backend.routes[oldRoute.Prefix]; exists {
		t.Fatalf("obsolete domain allocation route remains: %#v", backend.routes)
	}
	if err := runner.rollbackRoutes(context.Background(), transactionIDs); err != nil {
		t.Fatal(err)
	}
	if backend.routes[oldRoute.Prefix] != oldRoute {
		t.Fatalf("removed domain allocation route was not restored by rollback: %#v", backend.routes)
	}
}

func TestRefreshPolicyIsolatesFailedAutomaticDomainAndRetries(t *testing.T) {
	policyApplier := &recordingPolicy{
		capabilities:   proxy.Capabilities{IPv4: true, Domains: true, DomainMappings: true},
		rejectedDomain: "bad.example",
	}
	runner, stateStore := newTestRunner(t, policyApplier)
	runner.config.Acceleration.Enabled = true
	runner.config.Acceleration.AutoDiscover = true
	runner.config.Acceleration.AutoApply = true
	runner.config.Acceleration.ManualDomains = nil
	runner.domainResolver = staticDomainResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	runner.domainVerifier = &selectiveDomainVerifier{rejected: map[string]map[string]bool{}}
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.1", Family: 4, PolicyVerified: true}
		state.Nodes["1.1.1.1"] = store.NodeStats{Successes: 2, AverageScore: 99}
		state.Nodes["1.1.1.2"] = store.NodeStats{Successes: 2, AverageScore: 98}
		state.DiscoveredDomains["bad.example"] = store.DomainDiscovery{
			Domain: "bad.example", CloudflareVerified: true, PreflightVerified: true, Active: true,
			LastResolvedAddresses: []string{"1.1.1.1"},
		}
		state.DiscoveredDomains["good.example"] = store.DomainDiscovery{
			Domain: "good.example", CloudflareVerified: true, PreflightVerified: true, Active: true,
			LastResolvedAddresses: []string{"1.1.1.1"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := runner.RefreshPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(policyApplier.policies) != 2 {
		t.Fatalf("policy was not retried after isolating one domain: %#v", policyApplier.policies)
	}
	finalPolicy := policyApplier.policies[1]
	if len(finalPolicy.DomainMappings) != 1 || finalPolicy.DomainMappings[0].Domain != "good.example" || finalPolicy.DomainMappings[0].Addresses[0] != "1.1.1.1" {
		t.Fatalf("failed automatic domain remained in retried policy or its IP was not reused: %#v", finalPolicy.DomainMappings)
	}
	state := stateStore.Snapshot()
	failed := state.DiscoveredDomains["bad.example"]
	if failed.Active || failed.PreflightVerified || !strings.Contains(failed.LastError, "connection evidence mismatch") {
		t.Fatalf("failed automatic domain was not isolated: %#v", failed)
	}
	if !state.DiscoveredDomains["good.example"].Active {
		t.Fatalf("healthy automatic domain was disabled: %#v", state.DiscoveredDomains["good.example"])
	}
}

func TestReconfigureUpdatesIdleRunnerAndRejectsBusyRunner(t *testing.T) {
	runner, _ := newTestRunner(t, nil)
	next := runner.config
	next.Benchmark.Candidates = 3
	snapshot := ranges.Snapshot{Version: 1, Source: "reload", Hash: "reload", IPv4: []string{"1.1.1.0/30"}}
	refreshed, err := runner.Reconfigure(
		context.Background(), next, staticRanges{snapshot: snapshot}, staticBenchmark{}, nil,
		cfnetwork.PhysicalPath{}, nil, nil, nil, nil, false,
	)
	if err != nil || refreshed || runner.config.Benchmark.Candidates != 3 {
		t.Fatalf("idle Reconfigure() = refreshed %t, error %v, config %#v", refreshed, err, runner.config.Benchmark)
	}
	if !runner.operationGate.tryAcquire() {
		t.Fatal("failed to hold operation gate for busy reload test")
	}
	_, err = runner.Reconfigure(
		context.Background(), next, staticRanges{snapshot: snapshot}, staticBenchmark{}, nil,
		cfnetwork.PhysicalPath{}, nil, nil, nil, nil, false,
	)
	runner.operationGate.release()
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("busy Reconfigure() error = %v, want ErrAlreadyRunning", err)
	}
}

func TestReconfigureRestoresPreviousDependenciesWhenPolicyRefreshFails(t *testing.T) {
	runner, stateStore := newTestRunner(t, nil)
	previousConfig := runner.config
	previousRanges := runner.ranges
	previousBenchmark := runner.benchmark
	if err := stateStore.Update(func(state *store.State) error {
		state.CurrentIPv4 = &store.Selection{IP: "1.1.1.1", Family: 4, PolicyVerified: true}
		state.Policy = &store.PolicySnapshot{IPv4CIDRs: []string{"1.1.1.1/32"}, Receipts: json.RawMessage(`{"receipts":[]}`)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := previousConfig
	next.Benchmark.Candidates = 3
	_, err := runner.Reconfigure(
		context.Background(), next,
		staticRanges{snapshot: ranges.Snapshot{Version: 1, Source: "reload", Hash: "reload", IPv4: []string{"1.1.1.0/24"}}},
		staticBenchmark{}, nil, cfnetwork.PhysicalPath{}, failingPolicy{}, nil, nil, nil, true,
	)
	if err == nil {
		t.Fatal("policy refresh failure was not returned")
	}
	if !reflect.DeepEqual(runner.config, previousConfig) || !reflect.DeepEqual(runner.ranges, previousRanges) || !reflect.DeepEqual(runner.benchmark, previousBenchmark) || runner.policy != nil {
		t.Fatalf("failed reconfiguration retained new dependencies: config=%#v policy=%#v", runner.config, runner.policy)
	}
}

func newTestRunner(t *testing.T, policy PolicyApplier) (*Runner, *store.Store) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Benchmark.IPv4 = true
	cfg.Benchmark.IPv6 = false
	cfg.Benchmark.Candidates = 2
	cfg.Benchmark.DownloadTop = 1
	cfg.Acceleration.ManualDownloadTest = false
	stateStore, err := store.Open(cfg.DataDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ranges.Snapshot{Version: 1, Source: "test", Hash: "test", IPv4: []string{"1.1.1.0/30"}}
	runner, err := NewRunner(cfg, staticRanges{snapshot: snapshot}, staticBenchmark{}, stateStore, nil, cfnetwork.PhysicalPath{}, policy, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return runner, stateStore
}

const concurrencyTestTimeout = time.Second

// waitForSignal 等待并发测试进入指定阶段，超时后提供稳定失败信息。
func waitForSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(concurrencyTestTimeout):
		t.Fatal(message)
	}
}

// waitForResult 等待异步操作完成并返回其错误。
func waitForResult(t *testing.T, result <-chan error, message string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(concurrencyTestTimeout):
		t.Fatal(message)
		return nil
	}
}

// waitForPendingRun 等待手动优选完成排队登记。
func waitForPendingRun(t *testing.T, runner *Runner) {
	t.Helper()
	deadline := time.Now().Add(concurrencyTestTimeout)
	for runner.pendingRuns.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("manual run did not register as pending")
		}
		time.Sleep(time.Millisecond)
	}
}
