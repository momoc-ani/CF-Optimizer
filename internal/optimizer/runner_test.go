package optimizer

import (
	"context"
	"encoding/json"
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
