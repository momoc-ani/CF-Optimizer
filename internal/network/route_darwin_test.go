//go:build darwin

package network

import (
	"reflect"
	"testing"
)

func TestDarwinRouteDeleteArgumentsPreserveInterfaceScope(t *testing.T) {
	arguments := darwinRouteDeleteArguments(RouteSpec{Prefix: "162.159.45.141/32", Interface: "en0"})
	want := []string{"-n", "delete", "-inet", "-net", "162.159.45.141/32", "-ifscope", "en0"}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("delete arguments = %#v, want %#v", arguments, want)
	}
}

func TestDarwinRouteDeleteArgumentsOmitEmptyInterfaceScope(t *testing.T) {
	arguments := darwinRouteDeleteArguments(RouteSpec{Prefix: "2001:db8::/64"})
	want := []string{"-n", "delete", "-inet6", "-net", "2001:db8::/64"}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("delete arguments = %#v, want %#v", arguments, want)
	}
}
