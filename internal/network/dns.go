package network

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// DiscoverPhysicalDNSServers 返回物理接口实际配置的 DNS 地址，并以网关作为最后回退候选。
func DiscoverPhysicalDNSServers(ctx context.Context, path PhysicalPath, timeout time.Duration) ([]string, error) {
	if path.Interface == "" || path.InterfaceIndex <= 0 || timeout <= 0 {
		return nil, errors.New("physical interface and positive timeout are required for DNS discovery")
	}
	servers, platformErr := discoverPlatformDNSServers(ctx, path, timeout)
	servers = append(servers, path.GatewayIPv4, path.GatewayIPv6)
	servers = normalizeDNSServers(servers)
	if len(servers) == 0 {
		return nil, fmt.Errorf("no physical DNS server was found: %w", platformErr)
	}
	return servers, nil
}

// normalizeDNSServers 过滤无效、未指定和重复 DNS 地址，同时保留稳定顺序。
func normalizeDNSServers(servers []string) []string {
	seen := map[netip.Addr]struct{}{}
	result := make([]string, 0, len(servers))
	for _, rawServer := range servers {
		server, err := netip.ParseAddr(rawServer)
		if err != nil || server.IsUnspecified() || server.IsMulticast() {
			continue
		}
		server = server.Unmap()
		if _, exists := seen[server]; exists {
			continue
		}
		seen[server] = struct{}{}
		result = append(result, server.String())
	}
	return result
}
