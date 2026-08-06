package guard

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

type testPolicySource struct {
	desired DesiredPolicy
	exists  bool
}

func (s testPolicySource) CurrentDesiredPolicy(context.Context) (DesiredPolicy, bool, error) {
	return s.desired, s.exists, nil
}

type testMaintenanceExecutor struct {
	source DesiredPolicy
	err    error
	calls  int
}

func (e *testMaintenanceExecutor) TryExecute(ctx context.Context, revision string, action func(context.Context, DesiredPolicy) error) error {
	e.calls++
	if e.err != nil {
		return e.err
	}
	if revision != e.source.Revision {
		return ErrDesiredPolicyChanged
	}
	return action(ctx, e.source)
}

type testStatusSink struct {
	mu       sync.Mutex
	statuses []Status
	notify   chan struct{}
}

func (s *testStatusSink) SetPolicyGuardStatus(status Status) {
	s.mu.Lock()
	s.statuses = append(s.statuses, status)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *testStatusSink) waitFor(t *testing.T, state State) Status {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		for _, status := range s.statuses {
			if status.State == state {
				s.mu.Unlock()
				return status
			}
		}
		s.mu.Unlock()
		select {
		case <-deadline.C:
			t.Fatalf("未观察到守护状态 %s", state)
		case <-s.notify:
		}
	}
}

type testStrategy struct {
	mu            sync.Mutex
	observation   Observation
	healthy       bool
	planCalls     int
	applyCalls    int
	verifyCalls   int
	rollbackCalls int
}

func (s *testStrategy) ID() string                                   { return "test-core" }
func (s *testStrategy) Observe(context.Context) (Observation, error) { return s.observation, nil }
func (s *testStrategy) Inspect(context.Context, DesiredPolicy) (Inspection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.healthy {
		return Inspection{Healthy: true}, nil
	}
	return Inspection{DriftReasons: []string{"规则缺失"}}, nil
}
func (s *testStrategy) Plan(context.Context, DesiredPolicy, Inspection) (RepairPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.planCalls++
	return RepairPlan{ID: "plan-1", Target: s.ID()}, nil
}
func (s *testStrategy) Apply(context.Context, RepairPlan) (RepairReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyCalls++
	s.healthy = true
	return RepairReceipt{ID: "plan-1", Target: s.ID(), Changed: true}, nil
}
func (s *testStrategy) Verify(context.Context, DesiredPolicy, RepairReceipt) (Verification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifyCalls++
	return Verification{Verified: true, Direct: true}, nil
}
func (s *testStrategy) Rollback(context.Context, RepairReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollbackCalls++
	return nil
}

func TestSupervisorDoesNotRepairOfflineOrInactiveKernel(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		observation Observation
		state       State
	}{
		{name: "offline", observation: Observation{Activity: ActivityOffline}, state: StateOffline},
		{name: "inactive", observation: Observation{Online: true, Activity: ActivityInactive, Manageable: true}, state: StateStandby},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			strategy := &testStrategy{observation: testCase.observation}
			executor := &testMaintenanceExecutor{source: testDesiredPolicy()}
			sink := &testStatusSink{notify: make(chan struct{}, 16)}
			supervisor := newTestSupervisor(t, strategy, executor, sink)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- supervisor.Run(ctx) }()
			sink.waitFor(t, testCase.state)
			cancel()
			<-done
			if executor.calls != 0 || strategy.applyCalls != 0 {
				t.Fatalf("非活动内核触发了规则修复: executor=%d apply=%d", executor.calls, strategy.applyCalls)
			}
		})
	}
}

func TestSupervisorRepairsDriftWithoutChangingDesiredPolicy(t *testing.T) {
	desired := testDesiredPolicy()
	strategy := &testStrategy{observation: Observation{Online: true, Activity: ActivityActive, Manageable: true, Revision: "config-1"}}
	executor := &testMaintenanceExecutor{source: desired}
	sink := &testStatusSink{notify: make(chan struct{}, 32)}
	supervisor := newTestSupervisor(t, strategy, executor, sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	status := sink.waitFor(t, StateVerified)
	cancel()
	<-done
	if status.PolicyRevision != desired.Revision || executor.calls != 1 || strategy.planCalls != 1 || strategy.applyCalls != 1 || strategy.verifyCalls != 1 {
		t.Fatalf("规则修复生命周期不完整: status=%#v executor=%d plan=%d apply=%d verify=%d", status, executor.calls, strategy.planCalls, strategy.applyCalls, strategy.verifyCalls)
	}
}

func TestSupervisorDefersWhenMaintenanceIsBusy(t *testing.T) {
	strategy := &testStrategy{observation: Observation{Online: true, Activity: ActivityActive, Manageable: true, Revision: "config-1"}}
	executor := &testMaintenanceExecutor{source: testDesiredPolicy(), err: ErrMaintenanceBusy}
	sink := &testStatusSink{notify: make(chan struct{}, 32)}
	supervisor := newTestSupervisor(t, strategy, executor, sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	status := sink.waitFor(t, StateRetryWait)
	cancel()
	<-done
	if strategy.applyCalls != 0 || !errors.Is(executor.err, ErrMaintenanceBusy) || status.RetryAt == nil {
		t.Fatalf("维护忙时没有安全延后: %#v", status)
	}
}

func newTestSupervisor(t *testing.T, strategy Strategy, executor MaintenanceExecutor, sink StatusSink) *Supervisor {
	t.Helper()
	options := Options{OfflinePoll: 2 * time.Millisecond, ActivePoll: 2 * time.Millisecond, AuditInterval: time.Hour, StableDelay: 2 * time.Millisecond, RetryDelays: []time.Duration{5 * time.Millisecond}, FailureThreshold: 2}
	supervisor, err := NewSupervisor([]Strategy{strategy}, testPolicySource{desired: testDesiredPolicy(), exists: true}, executor, sink, slog.New(slog.NewTextHandler(io.Discard, nil)), options)
	if err != nil {
		t.Fatal(err)
	}
	return supervisor
}

func testDesiredPolicy() DesiredPolicy {
	return DesiredPolicy{Revision: "policy-1", Policy: proxy.DirectPolicy{Domains: []string{"example.com"}}, AppliedAt: time.Unix(1, 0).UTC()}
}
