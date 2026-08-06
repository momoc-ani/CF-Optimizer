package mihomo

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

const (
	mappedConnectionVerificationAttempts      = 2
	mappedConnectionVerificationRetryInterval = 500 * time.Millisecond
	mappedConnectionVerificationMaxBackoff    = 4 * time.Second
)

var errMixedPortUnavailable = errors.New("Mihomo mixed-port is unavailable")

// mappingNotPropagatedError 表示连接已建立，但 Mihomo 尚未暴露目标域名的 DIRECT 连接证据。
type mappingNotPropagatedError struct{ err error }

func (e *mappingNotPropagatedError) Error() string {
	if e == nil || e.err == nil {
		return "Mihomo mapping has not propagated"
	}
	return e.err.Error()
}

// Unwrap 保留传播检查的底层描述，便于上层记录完整原因。
func (e *mappingNotPropagatedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// verifyMappedConnections 通过 Mihomo mixed-port 建立真实 HTTPS 连接并核对控制 API 连接证据。
func (a *Adapter) verifyMappedConnections(ctx context.Context, mappings []proxy.DomainMapping) error {
	mixedPort, err := a.mixedPort(ctx)
	if err != nil {
		return err
	}
	proxyHost := a.controller.Hostname()
	if proxyHost == "localhost" {
		proxyHost = "127.0.0.1"
	}
	proxyAddress := net.JoinHostPort(proxyHost, strconv.Itoa(mixedPort))
	for _, mapping := range mappings {
		err := verifyWithTransientRetryWindow(ctx, a.verificationTimeout, a.verificationAttemptTimeout, a.verificationRetryInterval, a.verificationMaxAttempts, func(verifyContext context.Context) error {
			return a.verifyMappedConnection(verifyContext, proxyAddress, mapping)
		})
		if err != nil {
			kind := proxy.DomainVerificationCandidateUnreachable
			var propagationErr *mappingNotPropagatedError
			if errors.As(err, &propagationErr) {
				kind = proxy.DomainVerificationMappingNotPropagated
			}
			address := ""
			if len(mapping.Addresses) > 0 {
				address = mapping.Addresses[0]
			}
			return &proxy.DomainVerificationError{Domain: mapping.Domain, Address: address, Kind: kind, Err: err}
		}
	}
	return nil
}

// verifyWithTransientRetry 仅对连接超时增加一次独立尝试，确定性策略错误仍立即返回。
func verifyWithTransientRetry(ctx context.Context, timeout time.Duration, verify func(context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt < mappedConnectionVerificationAttempts; attempt++ {
		verifyContext, cancel := context.WithTimeout(ctx, timeout)
		lastErr = verify(verifyContext)
		cancel()
		if lastErr == nil {
			return nil
		}
		if attempt+1 == mappedConnectionVerificationAttempts || ctx.Err() != nil || !isTransientVerificationError(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

// verifyWithTransientRetryWindow 在总窗口内按单次超时和退避重试 Mihomo 连接验证。
func verifyWithTransientRetryWindow(ctx context.Context, total, attempt, retry time.Duration, maxAttempts int, verify func(context.Context) error) error {
	if total <= 0 {
		total = attempt
	}
	if attempt <= 0 || attempt > total {
		attempt = total
	}
	if retry <= 0 {
		retry = mappedConnectionVerificationRetryInterval
	}
	if maxAttempts < 1 {
		maxAttempts = mappedConnectionVerificationAttempts
	}
	totalContext, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	var lastErr error
	for current := 1; current <= maxAttempts; current++ {
		attemptContext, attemptCancel := context.WithTimeout(totalContext, attempt)
		lastErr = verify(attemptContext)
		attemptCancel()
		if lastErr == nil {
			return nil
		}
		if current == maxAttempts || totalContext.Err() != nil || !isTransientVerificationError(lastErr) {
			return lastErr
		}
		interval := retry
		for step := 1; step < current && interval < mappedConnectionVerificationMaxBackoff; step++ {
			if interval > mappedConnectionVerificationMaxBackoff/2 {
				interval = mappedConnectionVerificationMaxBackoff
				break
			}
			interval *= 2
		}
		timer := time.NewTimer(interval)
		select {
		case <-totalContext.Done():
			timer.Stop()
			return lastErr
		case <-timer.C:
		}
	}
	return lastErr
}

// isTransientVerificationError 识别可通过一次重试吸收的网络或上下文超时。
func isTransientVerificationError(err error) bool {
	var propagationErr *mappingNotPropagatedError
	if errors.As(err, &propagationErr) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && networkErr.Timeout()
}

// mixedPort 从活动 Mihomo 配置读取可用于真实 HTTPS 验证的混合代理端口。
func (a *Adapter) mixedPort(ctx context.Context) (int, error) {
	body, status, err := a.request(ctx, http.MethodGet, "/configs", nil)
	if err != nil {
		return 0, fmt.Errorf("read Mihomo runtime config: %w", err)
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("Mihomo configs endpoint returned %d", status)
	}
	var runtimeConfig struct {
		MixedPort int `json:"mixed-port"`
	}
	if err := json.Unmarshal(body, &runtimeConfig); err != nil {
		return 0, fmt.Errorf("decode Mihomo runtime config: %w", err)
	}
	if runtimeConfig.MixedPort < 1 || runtimeConfig.MixedPort > 65535 {
		return 0, fmt.Errorf("%w for connection verification", errMixedPortUnavailable)
	}
	return runtimeConfig.MixedPort, nil
}

// verifyMappedConnectionsViaTUN 在没有 mixed-port 时通过普通无代理 Socket 触发 TUN，并核对控制面 DIRECT 证据。
func (a *Adapter) verifyMappedConnectionsViaTUN(ctx context.Context, mappings []proxy.DomainMapping) error {
	for _, mapping := range mappings {
		err := verifyWithTransientRetryWindow(ctx, a.verificationTimeout, a.verificationAttemptTimeout, a.verificationRetryInterval, a.verificationMaxAttempts, func(verifyContext context.Context) error {
			return a.verifyTUNMappedConnection(verifyContext, mapping)
		})
		if err != nil {
			kind := proxy.DomainVerificationCandidateUnreachable
			var propagationErr *mappingNotPropagatedError
			if errors.As(err, &propagationErr) {
				kind = proxy.DomainVerificationMappingNotPropagated
			}
			address := ""
			if len(mapping.Addresses) > 0 {
				address = mapping.Addresses[0]
			}
			return &proxy.DomainVerificationError{Domain: mapping.Domain, Address: address, Kind: kind, Err: err}
		}
	}
	return nil
}

// verifyTUNMappedConnection 建立不读取代理环境的 HTTPS 连接，依赖 TUN 接管后再查询连接证据。
func (a *Adapter) verifyTUNMappedConnection(ctx context.Context, mapping proxy.DomainMapping) error {
	connection, err := (&net.Dialer{Timeout: a.config.Timeout.Duration()}).DialContext(ctx, "tcp", net.JoinHostPort(mapping.Domain, "443"))
	if err != nil {
		return fmt.Errorf("connect TUN path for %s: %w", mapping.Domain, err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	tlsConnection := tls.Client(connection, &tls.Config{ServerName: mapping.Domain, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("TUN TLS handshake for %s: %w", mapping.Domain, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://"+mapping.Domain+"/", nil)
	if err != nil {
		return err
	}
	request.Host = mapping.Domain
	request.Header.Set("Connection", "keep-alive")
	request.Header.Set("User-Agent", "CF-Optimizer/1")
	if err := request.Write(tlsConnection); err != nil {
		return fmt.Errorf("send TUN HTTPS verification request for %s: %w", mapping.Domain, err)
	}
	return verifyMappedHTTPSResponse(bufio.NewReader(tlsConnection), request, mapping.Domain, func() error {
		return a.verifyConnectionEvidence(ctx, mapping)
	})
}

// verifyMappedConnection 经 Mihomo 发起 HTTPS 请求，并确认 TLS、Host 和实际远端地址一致。
func (a *Adapter) verifyMappedConnection(ctx context.Context, proxyAddress string, mapping proxy.DomainMapping) error {
	dialer := &net.Dialer{Timeout: a.config.Timeout.Duration()}
	connection, err := dialer.DialContext(ctx, "tcp", proxyAddress)
	if err != nil {
		return fmt.Errorf("connect Mihomo mixed-port for %s: %w", mapping.Domain, err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	connectRequest, err := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+mapping.Domain+":443", nil)
	if err != nil {
		return err
	}
	connectRequest.Host = mapping.Domain + ":443"
	if err := connectRequest.Write(connection); err != nil {
		return fmt.Errorf("send Mihomo CONNECT for %s: %w", mapping.Domain, err)
	}
	reader := bufio.NewReader(connection)
	connectResponse, err := http.ReadResponse(reader, connectRequest)
	if err != nil {
		return fmt.Errorf("read Mihomo CONNECT for %s: %w", mapping.Domain, err)
	}
	if connectResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("Mihomo CONNECT for %s returned %s", mapping.Domain, connectResponse.Status)
	}

	tlsConnection := tls.Client(connection, &tls.Config{ServerName: mapping.Domain, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("Mihomo TLS handshake for %s: %w", mapping.Domain, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://"+mapping.Domain+"/", nil)
	if err != nil {
		return err
	}
	request.Host = mapping.Domain
	request.Header.Set("Connection", "keep-alive")
	request.Header.Set("User-Agent", "CF-Optimizer/1")
	if err := request.Write(tlsConnection); err != nil {
		return fmt.Errorf("send HTTPS verification request for %s: %w", mapping.Domain, err)
	}
	return verifyMappedHTTPSResponse(bufio.NewReader(tlsConnection), request, mapping.Domain, func() error {
		return a.verifyConnectionEvidence(ctx, mapping)
	})
}

// verifyMappedHTTPSResponse 先消费站点响应，再在连接仍存活时查询 Mihomo DIRECT 证据。
func verifyMappedHTTPSResponse(reader *bufio.Reader, request *http.Request, domain string, verifyEvidence func() error) error {
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return fmt.Errorf("read HTTPS verification response for %s: %w", domain, err)
	}
	_ = response.Body.Close()
	if response.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("HTTPS verification response for %s returned %s", domain, response.Status)
	}
	return verifyEvidence()
}

// verifyConnectionEvidence 核对控制 API 中最新连接确实命中精确域名 DIRECT 规则。
func (a *Adapter) verifyConnectionEvidence(ctx context.Context, mapping proxy.DomainMapping) error {
	body, status, err := a.request(ctx, http.MethodGet, "/connections", nil)
	if err != nil {
		return fmt.Errorf("read Mihomo connections: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("Mihomo connections endpoint returned %d", status)
	}
	var response struct {
		Connections []struct {
			Metadata struct {
				Host          string `json:"host"`
				DestinationIP string `json:"destinationIP"`
			} `json:"metadata"`
			Chains      []string `json:"chains"`
			Rule        string   `json:"rule"`
			RulePayload string   `json:"rulePayload"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decode Mihomo connections: %w", err)
	}
	allowed := make(map[netip.Addr]struct{}, len(mapping.Addresses))
	for _, rawAddress := range mapping.Addresses {
		if address, parseErr := netip.ParseAddr(rawAddress); parseErr == nil {
			allowed[address.Unmap()] = struct{}{}
		}
	}
	for _, connection := range response.Connections {
		if !strings.EqualFold(strings.TrimSuffix(connection.Metadata.Host, "."), mapping.Domain) {
			continue
		}
		address, parseErr := netip.ParseAddr(connection.Metadata.DestinationIP)
		if parseErr != nil {
			continue
		}
		if _, expected := allowed[address.Unmap()]; !expected {
			continue
		}
		for _, chain := range connection.Chains {
			if strings.EqualFold(chain, "DIRECT") {
				return nil
			}
		}
		return &mappingNotPropagatedError{err: fmt.Errorf("Mihomo connection for %s reached %s but was not DIRECT (rule=%s payload=%s)", mapping.Domain, address, connection.Rule, connection.RulePayload)}
	}
	return &mappingNotPropagatedError{err: fmt.Errorf("Mihomo did not expose an active DIRECT connection for %s to an optimized address", mapping.Domain)}
}
