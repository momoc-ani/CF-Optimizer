package generic

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"testing"

	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

type routeBackend struct {
	routes map[string]cfnetwork.RouteSpec
}

func (b *routeBackend) Replace(_ context.Context, route cfnetwork.RouteSpec) error {
	b.routes[route.Prefix] = route
	return nil
}
func (b *routeBackend) Delete(_ context.Context, route cfnetwork.RouteSpec) error {
	delete(b.routes, route.Prefix)
	return nil
}
func (b *routeBackend) Get(_ context.Context, prefix string) (cfnetwork.RouteSpec, error) {
	route, exists := b.routes[prefix]
	if !exists {
		return cfnetwork.RouteSpec{}, cfnetwork.ErrRouteNotFound
	}
	return route, nil
}
func (b *routeBackend) Resolve(_ context.Context, target netip.Addr) (cfnetwork.ResolvedRoute, error) {
	for _, route := range b.routes {
		if netip.MustParsePrefix(route.Prefix).Contains(target) {
			return cfnetwork.ResolvedRoute{RouteSpec: route, SourceAddress: "192.0.2.10"}, nil
		}
	}
	return cfnetwork.ResolvedRoute{}, cfnetwork.ErrRouteNotFound
}

func TestGenericRouteLifecycle(t *testing.T) {
	backend := &routeBackend{routes: map[string]cfnetwork.RouteSpec{}}
	controller, err := cfnetwork.NewRouteController(t.TempDir(), backend, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(controller, cfnetwork.PhysicalPath{Interface: "eth0", InterfaceIndex: 2, GatewayIPv4: "192.0.2.1"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	policy := proxy.DirectPolicy{IPv4CIDRs: []string{"1.1.1.1/32"}}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Verify(context.Background(), policy, receipt); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Rollback(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	if len(backend.routes) != 0 {
		t.Fatalf("route remains after rollback: %#v", backend.routes)
	}
}

func TestGenericRouteSkipsExactExistingRoute(t *testing.T) {
	route := cfnetwork.RouteSpec{Prefix: "1.1.1.1/32", Gateway: "192.0.2.1", Interface: "eth0", InterfaceIndex: 2, Metric: 5}
	backend := &routeBackend{routes: map[string]cfnetwork.RouteSpec{route.Prefix: route}}
	controller, err := cfnetwork.NewRouteController(t.TempDir(), backend, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(controller, cfnetwork.PhysicalPath{Interface: "eth0", InterfaceIndex: 2, GatewayIPv4: "192.0.2.1"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	policy := proxy.DirectPolicy{IPv4CIDRs: []string{route.Prefix}}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Changed || len(controller.Transactions()) != 0 {
		t.Fatalf("exact route was applied again: receipt=%#v transactions=%#v", receipt, controller.Transactions())
	}
}
