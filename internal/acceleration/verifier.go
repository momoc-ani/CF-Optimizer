package acceleration

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

const maximumResponseLine = 8 << 10

// Verifier 通过绑定物理接口的 HTTPS 请求验证 SNI、Host、目标地址和系统映射。
type Verifier struct {
	dial    cfnetwork.DialContextFunc
	timeout time.Duration
}

// NewVerifier 创建不读取任何代理环境变量的域名映射验证器。
func NewVerifier(dial cfnetwork.DialContextFunc, timeout time.Duration) (*Verifier, error) {
	if dial == nil || timeout <= 0 {
		return nil, errors.New("domain verifier dialer and positive timeout are required")
	}
	return &Verifier{dial: dial, timeout: timeout}, nil
}

// VerifyPreflight 逐个连接目标地址，同时保留域名 SNI 与 HTTP Host。
func (v *Verifier) VerifyPreflight(ctx context.Context, mappings []proxy.DomainMapping) error {
	for _, mapping := range mappings {
		for _, rawAddress := range mapping.Addresses {
			address, err := netip.ParseAddr(rawAddress)
			if err != nil {
				return err
			}
			if _, err := v.request(ctx, mapping.Domain, net.JoinHostPort(address.String(), "443")); err != nil {
				return fmt.Errorf("preflight %s via %s: %w", mapping.Domain, address, err)
			}
		}
	}
	return nil
}

// VerifyApplied 使用系统解析重新连接，并要求远端地址属于已应用的优选地址。
func (v *Verifier) VerifyApplied(ctx context.Context, mappings []proxy.DomainMapping) error {
	for _, mapping := range mappings {
		remote, err := v.request(ctx, mapping.Domain, net.JoinHostPort(mapping.Domain, "443"))
		if err != nil {
			return fmt.Errorf("verify applied mapping for %s: %w", mapping.Domain, err)
		}
		allowed := false
		for _, rawAddress := range mapping.Addresses {
			if remote == rawAddress {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("domain %s connected to %s instead of an optimized address", mapping.Domain, remote)
		}
	}
	return nil
}

// request 直连指定地址执行最小 HTTPS 请求，并返回握手后的真实远端 IP。
func (v *Verifier) request(ctx context.Context, domain, address string) (string, error) {
	requestContext, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	connection, err := v.dial(requestContext, "tcp", address)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	remoteHost, _, err := net.SplitHostPort(connection.RemoteAddr().String())
	if err != nil {
		return "", err
	}
	remote, err := netip.ParseAddr(strings.Trim(remoteHost, "[]"))
	if err != nil {
		return "", err
	}
	tlsConnection := tls.Client(connection, &tls.Config{ServerName: domain, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	if err := tlsConnection.HandshakeContext(requestContext); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(tlsConnection, "HEAD / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: CF-Optimizer/1\r\n\r\n", domain); err != nil {
		return "", err
	}
	line, err := bufio.NewReaderSize(tlsConnection, maximumResponseLine).ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(line, "HTTP/1.") {
		return "", fmt.Errorf("unexpected HTTPS response %q", strings.TrimSpace(line))
	}
	return remote.Unmap().String(), nil
}
