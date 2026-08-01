package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type lifecycleAdapter struct {
	name        string
	verifyError error
	rolledBack  bool
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
func (a *lifecycleAdapter) Verify(context.Context, DirectPolicy, Receipt) error { return a.verifyError }
func (a *lifecycleAdapter) Rollback(context.Context, Receipt) error {
	a.rolledBack = true
	return nil
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
