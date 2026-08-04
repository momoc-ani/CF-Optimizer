package network

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
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

// readResolvConf 读取并解析 resolv.conf 中有效且不重复的 nameserver 地址。
func readResolvConf(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseResolvConf(file)
}

// parseResolvConf 解析 resolv.conf 内容，并忽略注释、非法地址和重复项。
func parseResolvConf(reader io.Reader) ([]string, error) {
	var servers []string
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			servers = append(servers, fields[1])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return normalizeDNSServers(servers), nil
}
