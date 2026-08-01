package optimizer

import (
	"net/netip"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/benchmark"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

func TestDecideAppliesHysteresis(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	cfg := config.Default().Benchmark
	current := &store.Selection{IP: "1.1.1.1", Score: 80, SelectedAt: now.Add(-time.Hour)}
	results := []benchmark.Result{
		qualified("1.0.0.1", 90),
		qualified("1.1.1.1", 80),
	}
	decision := Decide(results, current, 4, cfg, now)
	if decision.ShouldSwitch || decision.Selected.IP.String() != current.IP || decision.Reason != "improvement-below-threshold" {
		t.Fatalf("unexpected decision: %#v", decision)
	}
	results[0].Score = 93
	decision = Decide(results, current, 4, cfg, now)
	if !decision.ShouldSwitch || decision.Selected.IP.String() != "1.0.0.1" {
		t.Fatalf("expected switch: %#v", decision)
	}
}

func TestDecideHonorsMinimumHoldAndFailures(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	cfg := config.Default().Benchmark
	current := &store.Selection{IP: "1.1.1.1", Score: 50, SelectedAt: now.Add(-time.Minute)}
	results := []benchmark.Result{qualified("1.0.0.1", 99), qualified("1.1.1.1", 50)}
	decision := Decide(results, current, 4, cfg, now)
	if decision.ShouldSwitch || decision.Reason != "minimum-hold-active" {
		t.Fatalf("hold was not honored: %#v", decision)
	}
	current.ConsecutiveFailures = cfg.FailureThreshold
	decision = Decide(results, current, 4, cfg, now)
	if !decision.ShouldSwitch || decision.Reason != "current-failure-threshold" {
		t.Fatalf("failure fallback was not honored: %#v", decision)
	}
}

func TestNoCandidateKeepsExistingPolicy(t *testing.T) {
	cfg := config.Default().Benchmark
	current := &store.Selection{IP: "1.1.1.1", Score: 80, SelectedAt: time.Now()}
	decision := Decide(nil, current, 4, cfg, time.Now())
	if decision.HasSelection || decision.ShouldSwitch {
		t.Fatalf("old policy must remain untouched: %#v", decision)
	}
}

func TestApplyHistoryUsesLimitedWeight(t *testing.T) {
	results := []benchmark.Result{qualified("1.1.1.1", 100)}
	ApplyHistory(results, map[string]store.NodeStats{"1.1.1.1": {Attempts: 10, AverageScore: 40}})
	if results[0].Score != 91 {
		t.Fatalf("unexpected smoothed score: %.2f", results[0].Score)
	}
}

func TestRecordResultsStartsAndClearsCooldown(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	cfg := config.Default().Benchmark
	nodes := map[string]store.NodeStats{}
	failed := benchmark.Result{IP: netip.MustParseAddr("1.1.1.1"), Family: 4}
	for range cfg.FailureThreshold {
		RecordResults(nodes, []benchmark.Result{failed}, cfg, now)
	}
	if !nodes["1.1.1.1"].CooldownUntil.Equal(now.Add(cfg.FailureCooldown.Duration())) {
		t.Fatalf("cooldown was not started: %#v", nodes["1.1.1.1"])
	}
	RecordResults(nodes, []benchmark.Result{qualified("1.1.1.1", 80)}, cfg, now.Add(time.Minute))
	if nodes["1.1.1.1"].FailureStreak != 0 || !nodes["1.1.1.1"].CooldownUntil.IsZero() {
		t.Fatalf("successful result did not clear cooldown: %#v", nodes["1.1.1.1"])
	}
}

func qualified(rawIP string, score float64) benchmark.Result {
	ip := netip.MustParseAddr(rawIP)
	family := 6
	if ip.Is4() {
		family = 4
	}
	return benchmark.Result{IP: ip, Family: family, Qualified: true, Successes: 4, Score: score}
}
