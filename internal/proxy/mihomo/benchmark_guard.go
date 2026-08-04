package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

const benchmarkConnectionPollInterval = 100 * time.Millisecond

// SetBenchmarkDialer 注入绑定物理接口的原始 Dialer，供测速保护连接验证使用。
func (a *Adapter) SetBenchmarkDialer(interfaceName string, dial cfnetwork.DialContextFunc) {
	a.benchmarkInterface = interfaceName
	a.benchmarkDial = dial
}

// VerifyBenchmarkPath 保持一个真实候选连接，并轮询控制 API 确认其为 DIRECT 或未被 Mihomo 接管。
func (a *Adapter) VerifyBenchmarkPath(ctx context.Context, targets []netip.Addr) (proxy.BenchmarkPathEvidence, error) {
	if a.benchmarkDial == nil || a.benchmarkInterface == "" {
		return proxy.BenchmarkPathEvidence{}, errors.New("physical benchmark Dialer is unavailable")
	}
	verifyContext, cancel := context.WithTimeout(ctx, a.config.Timeout.Duration())
	defer cancel()
	var lastDialError error
	for _, target := range targets {
		target = target.Unmap()
		if !target.IsValid() {
			continue
		}
		connection, err := a.benchmarkDial(verifyContext, "tcp", net.JoinHostPort(target.String(), "443"))
		if err != nil {
			lastDialError = err
			if verifyContext.Err() != nil {
				break
			}
			continue
		}
		evidence, verifyErr := a.pollBenchmarkConnection(verifyContext, connection, target)
		_ = connection.Close()
		return evidence, verifyErr
	}
	if lastDialError != nil {
		return proxy.BenchmarkPathEvidence{}, fmt.Errorf("open benchmark evidence connection: %w", lastDialError)
	}
	if err := verifyContext.Err(); err != nil {
		return proxy.BenchmarkPathEvidence{}, err
	}
	return proxy.BenchmarkPathEvidence{}, errors.New("no valid benchmark target is available")
}

// pollBenchmarkConnection 在 Socket 保持打开期间查找与本地源端口对应的 Mihomo 连接。
func (a *Adapter) pollBenchmarkConnection(ctx context.Context, connection net.Conn, target netip.Addr) (proxy.BenchmarkPathEvidence, error) {
	evidence := proxy.BenchmarkPathEvidence{
		Interface: a.benchmarkInterface, Target: target.String(), SocketBound: true,
		Verification: "mihomo_connection_not_observed",
	}
	localIP, localPort := connectionEndpoint(connection.LocalAddr())
	for {
		observed, found, err := a.inspectBenchmarkConnection(ctx, target, localIP, localPort)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return evidence, nil
			}
			return evidence, err
		}
		if found {
			observed.Interface = a.benchmarkInterface
			observed.Target = target.String()
			observed.SocketBound = true
			return observed, nil
		}
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return evidence, nil
			}
			return evidence, err
		}
		timer := time.NewTimer(benchmarkConnectionPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return evidence, nil
			}
			return evidence, ctx.Err()
		case <-timer.C:
		}
	}
}

// inspectBenchmarkConnection 仅接受目标地址和本地源端点都匹配的连接，避免误认其他进程流量。
func (a *Adapter) inspectBenchmarkConnection(ctx context.Context, target netip.Addr, localIP, localPort string) (proxy.BenchmarkPathEvidence, bool, error) {
	body, status, err := a.request(ctx, http.MethodGet, "/connections", nil)
	if err != nil {
		return proxy.BenchmarkPathEvidence{}, false, fmt.Errorf("read Mihomo benchmark connections: %w", err)
	}
	if status != http.StatusOK {
		return proxy.BenchmarkPathEvidence{}, false, fmt.Errorf("Mihomo connections endpoint returned %d", status)
	}
	var response struct {
		Connections []struct {
			Metadata struct {
				SourceIP        string `json:"sourceIP"`
				SourcePort      string `json:"sourcePort"`
				DestinationIP   string `json:"destinationIP"`
				DestinationPort string `json:"destinationPort"`
			} `json:"metadata"`
			Chains      []string `json:"chains"`
			Rule        string   `json:"rule"`
			RulePayload string   `json:"rulePayload"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return proxy.BenchmarkPathEvidence{}, false, fmt.Errorf("decode Mihomo benchmark connections: %w", err)
	}
	for _, connection := range response.Connections {
		destination, parseErr := netip.ParseAddr(connection.Metadata.DestinationIP)
		if parseErr != nil || destination.Unmap() != target || connection.Metadata.DestinationPort != "443" {
			continue
		}
		if localPort != "" && connection.Metadata.SourcePort != localPort {
			continue
		}
		if localIP != "" && connection.Metadata.SourceIP != "" && connection.Metadata.SourceIP != localIP {
			continue
		}
		evidence := proxy.BenchmarkPathEvidence{
			ProxyObserved: true, Rule: connection.Rule, RulePayload: connection.RulePayload,
			Verification: "mihomo_connection_not_direct",
		}
		for _, chain := range connection.Chains {
			if strings.EqualFold(chain, "DIRECT") {
				evidence.DirectVerified = true
				evidence.Verification = "mihomo_connection_direct"
				return evidence, true, nil
			}
		}
		return evidence, true, fmt.Errorf("Mihomo observed benchmark connection to %s but chain was not DIRECT (rule=%s payload=%s)", target, connection.Rule, connection.RulePayload)
	}
	return proxy.BenchmarkPathEvidence{}, false, nil
}

// connectionEndpoint 提取 Socket 本地端点，格式不可用时保守返回空值。
func connectionEndpoint(address net.Addr) (string, string) {
	if address == nil {
		return "", ""
	}
	host, port, err := net.SplitHostPort(address.String())
	if err != nil {
		return "", ""
	}
	return host, port
}
