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

	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

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
		verifyContext, cancel := context.WithTimeout(ctx, a.config.Timeout.Duration())
		err := a.verifyMappedConnection(verifyContext, proxyAddress, mapping)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
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
		return 0, errors.New("Mihomo mixed-port is unavailable for connection verification")
	}
	return runtimeConfig.MixedPort, nil
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
	if err := a.verifyConnectionEvidence(ctx, mapping); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReader(tlsConnection), request)
	if err != nil {
		return fmt.Errorf("read HTTPS verification response for %s: %w", mapping.Domain, err)
	}
	_ = response.Body.Close()
	return nil
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
		return fmt.Errorf("Mihomo connection for %s reached %s but was not DIRECT (rule=%s payload=%s)", mapping.Domain, address, connection.Rule, connection.RulePayload)
	}
	return fmt.Errorf("Mihomo did not expose an active DIRECT connection for %s to an optimized address", mapping.Domain)
}
