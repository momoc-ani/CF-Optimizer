//go:build windows

package network

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestPowerShellTimeoutUsesLongerCleanupDeadline(t *testing.T) {
	deadline := time.Now().Add(30 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	configured := 10 * time.Second
	if timeout := powerShellTimeout(ctx, configured); timeout < 29*time.Second {
		t.Fatalf("powerShellTimeout() = %s, want a deadline near 30s", timeout)
	}
}

func TestPowerShellTimeoutKeepsConfiguredLimitWithoutDeadline(t *testing.T) {
	if timeout := powerShellTimeout(context.Background(), 10*time.Second); timeout != 10*time.Second {
		t.Fatalf("powerShellTimeout() = %s, want 10s", timeout)
	}
}

func TestResolveWindowsInterfaceIndexRefreshesStaleIndexByName(t *testing.T) {
	byIndexCalled := false
	index, err := resolveWindowsInterfaceIndex(
		RouteSpec{Interface: "Ethernet", InterfaceIndex: 28},
		func(name string) (*net.Interface, error) {
			if name != "Ethernet" {
				t.Fatalf("unexpected interface name: %q", name)
			}
			return &net.Interface{Index: 24, Name: name}, nil
		},
		func(int) (*net.Interface, error) {
			byIndexCalled = true
			return nil, errors.New("stale index")
		},
	)
	if err != nil || index != 24 || byIndexCalled {
		t.Fatalf("stale index was not refreshed by name: index=%d by_index_called=%t error=%v", index, byIndexCalled, err)
	}
}

func TestResolveWindowsInterfaceIndexFallsBackToValidIndex(t *testing.T) {
	index, err := resolveWindowsInterfaceIndex(
		RouteSpec{Interface: "Renamed Ethernet", InterfaceIndex: 24},
		func(string) (*net.Interface, error) { return nil, errors.New("name missing") },
		func(index int) (*net.Interface, error) { return &net.Interface{Index: index}, nil },
	)
	if err != nil || index != 24 {
		t.Fatalf("valid fallback index was not used: index=%d error=%v", index, err)
	}
}

func TestResolveWindowsInterfaceIndexRejectsMissingInterface(t *testing.T) {
	_, err := resolveWindowsInterfaceIndex(
		RouteSpec{Interface: "Missing Ethernet", InterfaceIndex: 28},
		func(string) (*net.Interface, error) { return nil, errors.New("name missing") },
		func(int) (*net.Interface, error) { return nil, errors.New("index missing") },
	)
	if err == nil {
		t.Fatal("expected missing interface error")
	}
}

func TestDecodeWindowsResolvedRouteSelectsRouteAndSourceAddress(t *testing.T) {
	output := []byte(`[
		{"DestinationPrefix":null,"NextHop":null,"InterfaceAlias":"以太网 3","InterfaceIndex":27,"RouteMetric":null,"IPAddress":"192.168.15.116"},
		{"DestinationPrefix":"103.21.244.0/22","NextHop":"192.168.15.1","InterfaceAlias":"以太网 3","InterfaceIndex":27,"RouteMetric":5,"IPAddress":null}
	]`)

	resolved, err := decodeWindowsResolvedRoute(output)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Prefix != "103.21.244.0/22" || resolved.Gateway != "192.168.15.1" || resolved.SourceAddress != "192.168.15.116" {
		t.Fatalf("unexpected resolved route: %#v", resolved)
	}
}

func TestDecodeWindowsResolvedRouteRejectsMissingRouteRecord(t *testing.T) {
	output := []byte(`[{"InterfaceAlias":"以太网 3","InterfaceIndex":27,"IPAddress":"192.168.15.116"}]`)

	_, err := decodeWindowsResolvedRoute(output)
	if !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("expected ErrRouteNotFound, got %v", err)
	}
}
