package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"
)

type lifecycleAdapter struct {
	name                 string
	verifyError          error
	cancel               context.CancelFunc
	rolledBack           bool
	rollbackContextError error
}

type recordingReceiptJournal struct {
	begins   int
	recorded []Receipt
	removed  []Receipt
}

func (j *recordingReceiptJournal) Begin(DirectPolicy) error {
	j.begins++
	return nil
}

func (j *recordingReceiptJournal) Record(receipt Receipt) error {
	j.recorded = append(j.recorded, receipt)
	return nil
}

func (j *recordingReceiptJournal) Remove(receipts []Receipt) error {
	j.removed = append(j.removed, receipts...)
	return nil
}

func (a *lifecycleAdapter) Name() string { return a.name }
func (a *lifecycleAdapter) Capabilities() Capabilities {
	return Capabilities{IPv4: true, Rollback: true}
}
func (a *lifecycleAdapter) Detect(context.Context) (Detection, error) {
	return Detection{Present: true}, nil
}
func (a *lifecycleAdapter) Plan(_ context.Context, policy DirectPolicy) (Plan, error) {
	return Plan{ID: a.name, Adapter: a.name, Policy: policy, Payload: json.RawMessage(`{}`)}, nil
}
func (a *lifecycleAdapter) Apply(context.Context, Plan) (Receipt, error) {
	return Receipt{ID: a.name, Adapter: a.name, Changed: true, AppliedAt: time.Now(), Payload: json.RawMessage(`{}`)}, nil
}
func (a *lifecycleAdapter) Verify(context.Context, DirectPolicy, Receipt) error {
	if a.cancel != nil {
		a.cancel()
	}
	return a.verifyError
}
func (a *lifecycleAdapter) Rollback(ctx context.Context, _ Receipt) error {
	a.rolledBack = true
	a.rollbackContextError = ctx.Err()
	return nil
}
func (a *lifecycleAdapter) VerifyBenchmarkPath(context.Context, []netip.Addr) (BenchmarkPathEvidence, error) {
	return BenchmarkPathEvidence{Target: "1.1.1.1", SocketBound: true, ProxyObserved: true, DirectVerified: true, Verification: "test_direct"}, nil
}

func TestCoordinatorRollsBackInScopeOnVerificationFailure(t *testing.T) {
	first := &lifecycleAdapter{name: "first"}
	second := &lifecycleAdapter{name: "second", verifyError: errors.New("not active")}
	coordinator, err := NewCoordinator([]Adapter{first, second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Apply(context.Background(), DirectPolicy{IPv4CIDRs: []string{"1.1.1.1/32"}})
	if err == nil || !first.rolledBack || !second.rolledBack {
		t.Fatalf("expected both adapters to roll back: first=%v second=%v err=%v", first.rolledBack, second.rolledBack, err)
	}
}

func TestCoordinatorRollsBackWithCleanupContextAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	adapter := &lifecycleAdapter{name: "cancelled", verifyError: errors.New("cancelled during verify"), cancel: cancel}
	coordinator, err := NewCoordinator([]Adapter{adapter}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Apply(ctx, DirectPolicy{IPv4CIDRs: []string{"1.1.1.1/32"}}); err == nil {
		t.Fatal("expected verification failure")
	}
	if !adapter.rolledBack || adapter.rollbackContextError != nil {
		t.Fatalf("rollback inherited cancellation: rolled_back=%t context_error=%v", adapter.rolledBack, adapter.rollbackContextError)
	}
}

func TestCoordinatorRejectsUnsupportedPolicyField(t *testing.T) {
	adapter := &lifecycleAdapter{name: "ip-only"}
	coordinator, err := NewCoordinator([]Adapter{adapter}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Apply(context.Background(), DirectPolicy{Domains: []string{"example.com"}}); err == nil {
		t.Fatal("expected unsupported domain policy to be rejected")
	}
}

func TestCoordinatorBenchmarkGuardUsesReversibleAdapterLifecycle(t *testing.T) {
	adapter := &lifecycleAdapter{name: "guard"}
	coordinator, err := NewCoordinator([]Adapter{adapter}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.BeginBenchmarkGuard(
		context.Background(), DirectPolicy{IPv4CIDRs: []string{"1.1.1.1/32"}}, []netip.Addr{netip.MustParseAddr("1.1.1.1")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Receipts) != 1 || len(result.Evidence) != 1 || !result.Evidence[0].GuardApplied || adapter.rolledBack {
		t.Fatalf("benchmark guard was not applied and verified: %#v", result)
	}
	if err := coordinator.EndBenchmarkGuard(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if !adapter.rolledBack {
		t.Fatal("benchmark guard receipt was not rolled back")
	}
}

func TestCoordinatorJournalsReceiptBeforeVerificationAndRemovesItAfterRollback(t *testing.T) {
	adapter := &lifecycleAdapter{name: "journaled", verifyError: errors.New("not active")}
	journal := &recordingReceiptJournal{}
	coordinator, err := NewCoordinator([]Adapter{adapter}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	coordinator.SetReceiptJournal(journal)
	if _, err := coordinator.Apply(context.Background(), DirectPolicy{IPv4CIDRs: []string{"1.1.1.1/32"}}); err == nil {
		t.Fatal("expected verification failure")
	}
	if journal.begins != 1 || len(journal.recorded) != 1 || len(journal.removed) != 1 || journal.recorded[0].ID != "journaled" {
		t.Fatalf("unexpected receipt journal lifecycle: %#v", journal)
	}
}
