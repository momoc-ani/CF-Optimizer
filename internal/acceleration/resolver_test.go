package acceleration

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

// TestNewPhysicalResolverSelectsLookupNetwork 验证 DNS 查询类型与允许参与策略的地址族一致。
func TestNewPhysicalResolverSelectsLookupNetwork(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("not used")
	}
	tests := []struct {
		name    string
		options ResolverOptions
		network string
	}{
		{name: "IPv4 only", options: ResolverOptions{IPv4: true}, network: "ip4"},
		{name: "IPv6 only", options: ResolverOptions{IPv6: true}, network: "ip6"},
		{name: "dual stack", options: ResolverOptions{IPv4: true, IPv6: true}, network: "ip"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver, err := NewPhysicalResolver(dial, []string{"1.1.1.1"}, time.Second, test.options)
			if err != nil {
				t.Fatal(err)
			}
			if resolver.lookupNetwork != test.network {
				t.Fatalf("lookup network = %q, want %q", resolver.lookupNetwork, test.network)
			}
		})
	}
	if _, err := NewPhysicalResolver(dial, []string{"1.1.1.1"}, time.Second, ResolverOptions{}); err == nil {
		t.Fatal("resolver accepted no enabled address family")
	}
}

// TestNormalizeResolvedAddressesFiltersUnavailableFamilies 验证历史 AAAA 不会进入仅有 IPv4 物理出口的域名证据。
func TestNormalizeResolvedAddressesFiltersUnavailableFamilies(t *testing.T) {
	addresses := []netip.Addr{
		netip.MustParseAddr("2606:4700:3037::ac43:c0fd"),
		netip.MustParseAddr("172.67.192.253"),
		netip.MustParseAddr("104.21.92.119"),
		netip.MustParseAddr("172.67.192.253"),
	}
	if result := normalizeResolvedAddresses(addresses, true, false); !reflect.DeepEqual(result, []netip.Addr{
		netip.MustParseAddr("104.21.92.119"), netip.MustParseAddr("172.67.192.253"),
	}) {
		t.Fatalf("unexpected IPv4-only result: %v", result)
	}
	if result := normalizeResolvedAddresses(addresses, false, true); !reflect.DeepEqual(result, []netip.Addr{
		netip.MustParseAddr("2606:4700:3037::ac43:c0fd"),
	}) {
		t.Fatalf("unexpected IPv6-only result: %v", result)
	}
}
