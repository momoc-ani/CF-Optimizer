package network

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"
)

type memoryRouteBackend struct {
	routes      map[string]RouteSpec
	resolveFail bool
}

func newMemoryRouteBackend() *memoryRouteBackend {
	return &memoryRouteBackend{routes: map[string]RouteSpec{}}
}

func (b *memoryRouteBackend) Replace(_ context.Context, route RouteSpec) error {
	b.routes[route.Prefix] = route
	return nil
}

func (b *memoryRouteBackend) Delete(_ context.Context, route RouteSpec) error {
	if _, exists := b.routes[route.Prefix]; !exists {
		return ErrRouteNotFound
	}
	delete(b.routes, route.Prefix)
	return nil
}

func (b *memoryRouteBackend) Get(_ context.Context, prefix string) (RouteSpec, error) {
	route, exists := b.routes[prefix]
	if !exists {
		return RouteSpec{}, ErrRouteNotFound
	}
	return route, nil
}

func (b *memoryRouteBackend) Resolve(_ context.Context, target netip.Addr) (ResolvedRoute, error) {
	if b.resolveFail {
		return ResolvedRoute{}, errors.New("forced lookup failure")
	}
	for _, route := range b.routes {
		if netip.MustParsePrefix(route.Prefix).Contains(target) {
			return ResolvedRoute{RouteSpec: route, SourceAddress: "192.0.2.10"}, nil
		}
	}
	return ResolvedRoute{}, ErrRouteNotFound
}

func TestRouteApplyVerifyAndRemove(t *testing.T) {
	backend := newMemoryRouteBackend()
	controller := newTestRouteController(t, backend)
	route := testRoute()
	plan, err := controller.Plan(context.Background(), route, false)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := controller.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if transaction.State != "verified" || transaction.Verification.SourceAddress == "" {
		t.Fatalf("route was not verified: %#v", transaction)
	}
	removed, err := controller.Remove(context.Background(), route)
	if err != nil || removed.State != "verified" {
		t.Fatalf("route was not removed: %#v, %v", removed, err)
	}
}

func TestRouteVerificationFailureRollsBack(t *testing.T) {
	backend := newMemoryRouteBackend()
	previous := testRoute()
	previous.Gateway = "192.0.2.2"
	backend.routes[previous.Prefix] = previous
	controller := newTestRouteController(t, backend)
	plan, err := controller.Plan(context.Background(), testRoute(), false)
	if err != nil {
		t.Fatal(err)
	}
	backend.resolveFail = true
	transaction, err := controller.Apply(context.Background(), plan)
	if err == nil || transaction.State != "rolled_back" {
		t.Fatalf("expected rollback: %#v, %v", transaction, err)
	}
	if backend.routes[previous.Prefix].Gateway != previous.Gateway {
		t.Fatalf("previous route was not restored: %#v", backend.routes)
	}
}

func TestRecoverRemovesVerifiedTemporaryRoute(t *testing.T) {
	backend := newMemoryRouteBackend()
	controller := newTestRouteController(t, backend)
	plan, _ := controller.Plan(context.Background(), testRoute(), true)
	if _, err := controller.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewRouteController(controller.pathDir(), backend, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Get(context.Background(), testRoute().Prefix); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("temporary route remains: %v", err)
	}
}

func newTestRouteController(t *testing.T, backend RouteBackend) *RouteController {
	t.Helper()
	controller, err := NewRouteController(t.TempDir(), backend, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	controller.now = func() time.Time { return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC) }
	return controller
}

func (c *RouteController) pathDir() string {
	return filepath.Dir(c.path)
}

func testRoute() RouteSpec {
	return RouteSpec{Prefix: "1.1.1.1/32", Gateway: "192.0.2.1", Interface: "eth0", InterfaceIndex: 2, Metric: 5}
}
