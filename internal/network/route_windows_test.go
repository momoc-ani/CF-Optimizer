//go:build windows

package network

import (
	"errors"
	"testing"
)

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
