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
	appliedVerificationMaxBackoff    = 4 * time.Second
)

type domainRequestFunc func(context.Context, string, string) (string, error)

// VerificationOptions 定义预检和应用后域名映射验证的独立时间窗口。
type VerificationOptions struct {
	PreflightTimeout time.Duration
	ApplyTimeout     time.Duration
	AttemptTimeout   time.Duration
	RetryInterval    time.Duration
	MaxAttempts      int
}

// Verifier 通过绑定物理接口的 HTTPS 请求验证 SNI、Host、目标地址和系统映射。
type Verifier struct {
	dial                  cfnetwork.DialContextFunc
	timeout               time.Duration
	appliedTimeout        time.Duration
	appliedAttemptTimeout time.Duration
	requestConnection     domainRequestFunc
	appliedMaxAttempts    int
	appliedRetryInterval  time.Duration
}

// NewVerifier 创建不读取任何代理环境变量的域名映射验证器。
func NewVerifier(dial cfnetwork.DialContextFunc, timeout time.Duration) (*Verifier, error) {
	return NewVerifierWithOptions(dial, VerificationOptions{
		PreflightTimeout: timeout,
		ApplyTimeout:     timeout,
		AttemptTimeout:   timeout,
		RetryInterval:    appliedVerificationRetryInterval,
		MaxAttempts:      appliedVerificationMaxAttempts,
	})
}

// NewVerifierWithOptions 创建具有独立预检和应用验证窗口的域名映射验证器。
func NewVerifierWithOptions(dial cfnetwork.DialContextFunc, options VerificationOptions) (*Verifier, error) {
	if dial == nil || options.PreflightTimeout <= 0 || options.ApplyTimeout <= 0 || options.AttemptTimeout <= 0 || options.RetryInterval <= 0 {
		return nil, errors.New("domain verifier dialer and positive verification options are required")
	}
	if options.AttemptTimeout > options.ApplyTimeout {
		return nil, errors.New("domain verifier attempt timeout must not exceed apply timeout")
	}
	if options.MaxAttempts < 1 || options.MaxAttempts > 20 {
		return nil, errors.New("domain verifier max attempts must be between 1 and 20")
	}
	verifier := &Verifier{
		dial:                  dial,
		timeout:               options.PreflightTimeout,
		appliedTimeout:        options.ApplyTimeout,
		appliedAttemptTimeout: options.AttemptTimeout,
		appliedMaxAttempts:    options.MaxAttempts,
		appliedRetryInterval:  options.RetryInterval,
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
				return &proxy.DomainVerificationError{Domain: mapping.Domain, Address: rawAddress, Kind: proxy.DomainVerificationCandidateUnreachable, Err: err}
			}
			requestContext, cancel := context.WithTimeout(ctx, v.timeout)
			_, requestErr := v.requestConnection(requestContext, mapping.Domain, net.JoinHostPort(address.String(), "443"))
			cancel()
			if requestErr != nil {
				return &proxy.DomainVerificationError{Domain: mapping.Domain, Address: address.String(), Kind: proxy.DomainVerificationCandidateUnreachable, Err: fmt.Errorf("preflight via %s: %w", address, requestErr)}
			}
		}
	}
	return nil
}

// VerifyApplied 使用系统解析重新连接，并要求远端地址属于已应用的优选地址。
func (v *Verifier) VerifyApplied(ctx context.Context, mappings []proxy.DomainMapping) error {
	for _, mapping := range mappings {
		if err := v.verifyAppliedMapping(ctx, mapping); err != nil {
			var verificationErr *proxy.DomainVerificationError
			if errors.As(err, &verificationErr) {
				return verificationErr
			}
			return &proxy.DomainVerificationError{Domain: mapping.Domain, Kind: proxy.DomainVerificationCandidateUnreachable, Err: err}
		}
	}
	return nil
}

// verifyAppliedMapping 在单个总超时窗口内重试系统映射，吸收 Hosts 更新后的短暂解析缓存延迟。
func (v *Verifier) verifyAppliedMapping(ctx context.Context, mapping proxy.DomainMapping) error {
	verificationContext, cancel := context.WithTimeout(ctx, v.appliedTimeout)
	defer cancel()
	address := net.JoinHostPort(mapping.Domain, "443")
	candidateAddress := ""
	if len(mapping.Addresses) > 0 {
		candidateAddress = mapping.Addresses[0]
	}
	var lastErr error
	lastKind := proxy.DomainVerificationCandidateUnreachable
	for attempt := 1; attempt <= v.appliedMaxAttempts; attempt++ {
		attemptContext, attemptCancel := context.WithTimeout(verificationContext, v.appliedAttemptTimeout)
		remote, err := v.requestConnection(attemptContext, mapping.Domain, address)
		attemptCancel()
		if err != nil {
			lastKind = proxy.DomainVerificationCandidateUnreachable
			lastErr = fmt.Errorf("candidate %s HTTPS connection unavailable: %w", candidateAddress, err)
		} else if mappingContainsAddress(mapping, remote) {
			return nil
		} else {
			lastKind = proxy.DomainVerificationMappingNotPropagated
			lastErr = fmt.Errorf("connected to %s instead of an optimized address", remote)
		}
		if attempt == v.appliedMaxAttempts {
			break
		}
		if err := waitForAppliedRetry(verificationContext, appliedRetryInterval(v.appliedRetryInterval, attempt)); err != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = verificationContext.Err()
	}
	return &proxy.DomainVerificationError{
		Domain: mapping.Domain, Address: candidateAddress, Kind: lastKind, Err: lastErr,
	}
}

// appliedRetryInterval 使用指数退避限制重复请求密度，避免在总窗口内挤压请求。
func appliedRetryInterval(base time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	interval := base
	for step := 1; step < attempt && interval < appliedVerificationMaxBackoff; step++ {
		if interval > appliedVerificationMaxBackoff/2 {
			return appliedVerificationMaxBackoff
		}
		interval *= 2
	}
	if interval > appliedVerificationMaxBackoff {
		return appliedVerificationMaxBackoff
	}
	return interval
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
	requestContext := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		requestContext, cancel = context.WithTimeout(ctx, v.timeout)
		defer cancel()
	}
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
