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
	dial       cfnetwork.DialContextFunc
	servers    []string
	queryLimit time.Duration
}

// NewPhysicalResolver 创建不读取系统代理设置的物理 DNS 解析器。
func NewPhysicalResolver(dial cfnetwork.DialContextFunc, servers []string, timeout time.Duration) (*PhysicalResolver, error) {
	if dial == nil || len(servers) == 0 || timeout <= 0 {
		return nil, errors.New("physical resolver dialer, DNS servers, and positive timeout are required")
	}
	serverAddresses := make([]string, 0, len(servers))
	for _, server := range servers {
		address, err := netip.ParseAddr(server)
		if err != nil {
			return nil, fmt.Errorf("parse physical DNS server %q: %w", server, err)
		}
		serverAddresses = append(serverAddresses, net.JoinHostPort(address.String(), "53"))
	}
	return &PhysicalResolver{dial: dial, servers: serverAddresses, queryLimit: timeout}, nil
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
	addresses, err := resolver.LookupNetIP(queryContext, "ip", domain)
	if err != nil {
		return nil, err
	}
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if address.IsGlobalUnicast() {
			seen[address] = struct{}{}
		}
	}
	result := make([]netip.Addr, 0, len(seen))
	for address := range seen {
		result = append(result, address)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Compare(result[j]) < 0 })
	return result, nil
}
