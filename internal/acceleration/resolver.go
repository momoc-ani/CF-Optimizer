package acceleration

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"time"

	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
)

// PhysicalResolver 通过绑定物理接口的系统 DNS 查询规避代理 Fake-IP 结果。
type PhysicalResolver struct {
	dial          cfnetwork.DialContextFunc
	servers       []string
	queryLimit    time.Duration
	lookupNetwork string
	ipv4          bool
	ipv6          bool
}

// ResolverOptions 限定物理 DNS 结果允许参与域名验证的地址族。
type ResolverOptions struct {
	IPv4 bool
	IPv6 bool
}

// NewPhysicalResolver 创建不读取系统代理设置的物理 DNS 解析器。
func NewPhysicalResolver(dial cfnetwork.DialContextFunc, servers []string, timeout time.Duration, options ResolverOptions) (*PhysicalResolver, error) {
	if dial == nil || len(servers) == 0 || timeout <= 0 {
		return nil, errors.New("physical resolver dialer, DNS servers, and positive timeout are required")
	}
	if !options.IPv4 && !options.IPv6 {
		return nil, errors.New("physical resolver requires at least one enabled address family")
	}
	serverAddresses := make([]string, 0, len(servers))
	for _, server := range servers {
		address, err := netip.ParseAddr(server)
		if err != nil {
			return nil, fmt.Errorf("parse physical DNS server %q: %w", server, err)
		}
		serverAddresses = append(serverAddresses, net.JoinHostPort(address.String(), "53"))
	}
	lookupNetwork := "ip"
	if options.IPv4 && !options.IPv6 {
		lookupNetwork = "ip4"
	} else if options.IPv6 && !options.IPv4 {
		lookupNetwork = "ip6"
	}
	return &PhysicalResolver{
		dial: dial, servers: serverAddresses, queryLimit: timeout,
		lookupNetwork: lookupNetwork, ipv4: options.IPv4, ipv6: options.IPv6,
	}, nil
}

// Resolve 依次查询物理接口 DNS，返回首个成功响应中的稳定去重地址。
func (r *PhysicalResolver) Resolve(ctx context.Context, domain string) ([]netip.Addr, error) {
	var queryErrors []error
	for _, server := range r.servers {
		addresses, err := r.query(ctx, domain, server)
		if err != nil {
			queryErrors = append(queryErrors, fmt.Errorf("query physical DNS %s: %w", server, err))
			continue
		}
		if len(addresses) > 0 {
			return addresses, nil
		}
	}
	return nil, errors.Join(queryErrors...)
}

// query 通过指定物理 DNS 服务器解析域名，并过滤无效或重复地址。
func (r *PhysicalResolver) query(ctx context.Context, domain, server string) ([]netip.Addr, error) {
	queryContext, cancel := context.WithTimeout(ctx, r.queryLimit)
	defer cancel()
	resolver := &net.Resolver{
		PreferGo:     true,
		StrictErrors: false,
		Dial: func(dialContext context.Context, network, _ string) (net.Conn, error) {
			return r.dial(dialContext, network, server)
		},
	}
	addresses, err := resolver.LookupNetIP(queryContext, r.lookupNetwork, domain)
	if err != nil {
		return nil, err
	}
	return normalizeResolvedAddresses(addresses, r.ipv4, r.ipv6), nil
}

// normalizeResolvedAddresses 过滤当前物理路径不可用的地址族，并稳定去重排序。
func normalizeResolvedAddresses(addresses []netip.Addr, ipv4, ipv6 bool) []netip.Addr {
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if address.IsGlobalUnicast() && ((address.Is4() && ipv4) || (address.Is6() && ipv6)) {
			seen[address] = struct{}{}
		}
	}
	result := make([]netip.Addr, 0, len(seen))
	for address := range seen {
		result = append(result, address)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Compare(result[j]) < 0 })
	return result
}
