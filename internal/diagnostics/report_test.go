package diagnostics

import (
	"context"
	"net"
	"net/netip"
	"testing"

	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
)

type diagnosticBackend struct{ route cfnetwork.ResolvedRoute }

func (d diagnosticBackend) Replace(context.Context, cfnetwork.RouteSpec) error { return nil }
func (d diagnosticBackend) Delete(context.Context, cfnetwork.RouteSpec) error  { return nil }
func (d diagnosticBackend) Get(context.Context, string) (cfnetwork.RouteSpec, error) {
	return d.route.RouteSpec, nil
}
func (d diagnosticBackend) Resolve(context.Context, netip.Addr) (cfnetwork.ResolvedRoute, error) {
	return d.route, nil
}

type diagnosticConn struct{ net.Conn }

func (diagnosticConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 50123}
}
func (diagnosticConn) Close() error { return nil }

func TestVerifyRouteRequiresAllEvidence(t *testing.T) {
	path := cfnetwork.PhysicalPath{Interface: "eth0", InterfaceIndex: 2, GatewayIPv4: "192.0.2.1", SourceIPv4: []string{"192.0.2.10"}}
	backend := diagnosticBackend{route: cfnetwork.ResolvedRoute{RouteSpec: cfnetwork.RouteSpec{Gateway: "192.0.2.1", Interface: "eth0", InterfaceIndex: 2}}}
	dial := func(context.Context, string, string) (net.Conn, error) { return diagnosticConn{}, nil }
	evidence := verifyRoute(context.Background(), netip.MustParseAddr("1.1.1.1"), path, backend, dial)
	if !evidence.VerifiedDirect {
		t.Fatalf("expected verified evidence: %#v", evidence)
	}
	path.GatewayIPv4 = "192.0.2.254"
	evidence = verifyRoute(context.Background(), netip.MustParseAddr("1.1.1.1"), path, backend, dial)
	if evidence.VerifiedDirect {
		t.Fatalf("mismatched gateway must not be verified: %#v", evidence)
	}
}

func TestFilterProxyProcessesIsStableAndDeduplicated(t *testing.T) {
	values := filterProxyProcesses([]string{"mihomo", "other", "Mihomo", "sing-box"})
	if len(values) != 2 {
		t.Fatalf("unexpected process list: %v", values)
	}
}
