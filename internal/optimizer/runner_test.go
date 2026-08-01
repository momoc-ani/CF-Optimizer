package optimizer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

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

type recordingPolicy struct {
	policies  []proxy.DirectPolicy
	rollbacks []proxy.ApplyResult
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

func (*recordingPolicy) Capabilities() proxy.Capabilities {
	return proxy.Capabilities{IPv4: true}
}

func (p *recordingPolicy) Apply(_ context.Context, policy proxy.DirectPolicy) (proxy.ApplyResult, error) {
	p.policies = append(p.policies, policy)
	payload, _ := json.Marshal(policy)
	return proxy.ApplyResult{Receipts: []proxy.Receipt{{ID: "test", Adapter: "test", Changed: true, Payload: payload}}}, nil
}

func (p *recordingPolicy) Rollback(_ context.Context, applied proxy.ApplyResult) error {
	p.rollbacks = append(p.rollbacks, applied)
	return nil
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

func TestRunnerAppliesAndPersistsVerifiedSelection(t *testing.T) {
	policy := &recordingPolicy{}
	runner, stateStore := newTestRunner(t, policy)
	report, err := runner.Run(context.Background(), RunOptions{ApplyPolicy: true}, nil)
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

func TestRollbackRoutesUsesIndependentCleanupTimeouts(t *testing.T) {
	backend := &delayedRouteBackend{routes: map[string]cfnetwork.RouteSpec{}, delay: 5 * time.Millisecond}
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
	cfg.Network.CommandTimeout = config.Duration(20 * time.Millisecond)
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

func newTestRunner(t *testing.T, policy PolicyApplier) (*Runner, *store.Store) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Benchmark.IPv4 = true
	cfg.Benchmark.IPv6 = false
	cfg.Benchmark.Candidates = 2
	cfg.Benchmark.DownloadTop = 1
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
