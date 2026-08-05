package acceleration

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

const (
	maximumPreflightBody             = 64 << 10
	appliedVerificationMaxAttempts   = 4
	appliedVerificationRetryInterval = 250 * time.Millisecond
)

type domainRequestFunc func(context.Context, string, string) (string, error)

// Verifier 通过绑定物理接口的 HTTPS 请求验证 SNI、Host、目标地址和系统映射。
type Verifier struct {
	dial                 cfnetwork.DialContextFunc
	timeout              time.Duration
	requestConnection    domainRequestFunc
	appliedMaxAttempts   int
	appliedRetryInterval time.Duration
}

// NewVerifier 创建不读取任何代理环境变量的域名映射验证器。
func NewVerifier(dial cfnetwork.DialContextFunc, timeout time.Duration) (*Verifier, error) {
	if dial == nil || timeout <= 0 {
		return nil, errors.New("domain verifier dialer and positive timeout are required")
	}
	verifier := &Verifier{
		dial:                 dial,
		timeout:              timeout,
		appliedMaxAttempts:   appliedVerificationMaxAttempts,
		appliedRetryInterval: appliedVerificationRetryInterval,
	}
	verifier.requestConnection = verifier.request
	return verifier, nil
}

// VerifyPreflight 逐个连接目标地址，同时保留域名 SNI 与 HTTP Host。
func (v *Verifier) VerifyPreflight(ctx context.Context, mappings []proxy.DomainMapping) error {
	for _, mapping := range mappings {
		for _, rawAddress := range mapping.Addresses {
			address, err := netip.ParseAddr(rawAddress)
			if err != nil {
				return &proxy.DomainVerificationError{Domain: mapping.Domain, Err: err}
			}
			if _, err := v.requestConnection(ctx, mapping.Domain, net.JoinHostPort(address.String(), "443")); err != nil {
				return &proxy.DomainVerificationError{Domain: mapping.Domain, Err: fmt.Errorf("preflight via %s: %w", address, err)}
			}
		}
	}
	return nil
}

// VerifyApplied 使用系统解析重新连接，并要求远端地址属于已应用的优选地址。
func (v *Verifier) VerifyApplied(ctx context.Context, mappings []proxy.DomainMapping) error {
	for _, mapping := range mappings {
		if err := v.verifyAppliedMapping(ctx, mapping); err != nil {
			return &proxy.DomainVerificationError{Domain: mapping.Domain, Err: err}
		}
	}
	return nil
}

// verifyAppliedMapping 在单个总超时窗口内重试系统映射，吸收 Hosts 更新后的短暂解析缓存延迟。
func (v *Verifier) verifyAppliedMapping(ctx context.Context, mapping proxy.DomainMapping) error {
	verificationContext, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	address := net.JoinHostPort(mapping.Domain, "443")
	var lastErr error
	for attempt := 1; attempt <= v.appliedMaxAttempts; attempt++ {
		remote, err := v.requestConnection(verificationContext, mapping.Domain, address)
		if err != nil {
			lastErr = fmt.Errorf("verify applied mapping: %w", err)
		} else if mappingContainsAddress(mapping, remote) {
			return nil
		} else {
			lastErr = fmt.Errorf("connected to %s instead of an optimized address", remote)
		}
		if attempt == v.appliedMaxAttempts {
			break
		}
		if err := waitForAppliedRetry(verificationContext, v.appliedRetryInterval); err != nil {
			return errors.Join(lastErr, fmt.Errorf("wait for applied mapping propagation: %w", err))
		}
	}
	return lastErr
}

// mappingContainsAddress 判断真实远端是否属于当前域名允许的优选地址集合。
func mappingContainsAddress(mapping proxy.DomainMapping, remote string) bool {
	for _, rawAddress := range mapping.Addresses {
		if remote == rawAddress {
			return true
		}
	}
	return false
}

// waitForAppliedRetry 等待下一次系统映射验证，同时及时响应任务取消和总超时。
func waitForAppliedRetry(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
	if deadline, ok := requestContext.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return "", fmt.Errorf("set domain verification deadline: %w", err)
		}
	}
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
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, "https://"+domain+"/", nil)
	if err != nil {
		return "", err
	}
	request.Host = domain
	request.Header.Set("Connection", "close")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "CF-Optimizer/1")
	if err := request.Write(tlsConnection); err != nil {
		return "", err
	}
	response, err := http.ReadResponse(bufio.NewReader(tlsConnection), request)
	if err != nil {
		return "", err
	}
	if err := validateHTTPSResponse(response); err != nil {
		return "", err
	}
	return remote.Unmap().String(), nil
}

// validateHTTPSResponse 拒绝 Cloudflare 明确报告目标边缘地址无权承载域名的错误页。
func validateHTTPSResponse(response *http.Response) error {
	if response == nil || response.Body == nil {
		return errors.New("HTTPS response body is unavailable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumPreflightBody))
	if err != nil {
		return fmt.Errorf("read HTTPS response: %w", err)
	}
	normalized := strings.ToLower(string(body))
	if strings.Contains(normalized, "error 1034") && strings.Contains(normalized, "edge ip restricted") {
		return fmt.Errorf("Cloudflare returned %s with Error 1034 Edge IP Restricted", response.Status)
	}
	return nil
}
