package diagnostics

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
)

// RouteEvidence 保存路由查询、Socket 源地址和预期物理出口的逐项比对结果。
type RouteEvidence struct {
	Target           string                  `json:"target"`
	Resolved         cfnetwork.ResolvedRoute `json:"resolved"`
	SocketSource     string                  `json:"socket_source,omitempty"`
	SocketConnected  bool                    `json:"socket_connected"`
	InterfaceMatches bool                    `json:"interface_matches"`
	GatewayMatches   bool                    `json:"gateway_matches"`
	SourceMatches    bool                    `json:"source_matches"`
	VerifiedDirect   bool                    `json:"verified_direct"`
	Error            string                  `json:"error,omitempty"`
}

// Report 汇总 test-route 需要展示的物理路径、虚拟接口和代理检测信息。
type Report struct {
	GeneratedAt            time.Time              `json:"generated_at"`
	Platform               string                 `json:"platform"`
	PhysicalPath           cfnetwork.PhysicalPath `json:"physical_path"`
	Route                  RouteEvidence          `json:"route"`
	VirtualInterfaces      []string               `json:"virtual_interfaces"`
	DetectedProxyProcesses []string               `json:"detected_proxy_processes"`
	ProxyEnvironmentSet    bool                   `json:"proxy_environment_set"`
	DirectPolicyVerified   bool                   `json:"direct_policy_verified"`
	Warnings               []string               `json:"warnings"`
}

// Generate 通过实际路由查询和一次 TCP 连接生成诊断报告，但不修改系统配置。
func Generate(ctx context.Context, target netip.Addr, path cfnetwork.PhysicalPath, backend cfnetwork.RouteBackend, dial cfnetwork.DialContextFunc, processTimeout time.Duration) Report {
	report := Report{
		GeneratedAt: time.Now().UTC(), Platform: runtime.GOOS, PhysicalPath: path,
		ProxyEnvironmentSet: proxyEnvironmentIsSet(),
		Warnings:            []string{"诊断无法证明第三方 VPN Kill Switch 或内核过滤规则一定允许直连。"},
	}
	report.VirtualInterfaces = findVirtualInterfaces()
	processes, err := detectProxyProcesses(ctx, processTimeout)
	if err != nil {
		report.Warnings = append(report.Warnings, "代理进程检测失败："+err.Error())
	} else {
		report.DetectedProxyProcesses = processes
	}
	report.Route = verifyRoute(ctx, target, path, backend, dial)
	if report.Route.VerifiedDirect {
		report.Warnings = append(report.Warnings, "已验证系统选路和 Socket 源地址；透明代理仍需结合代理 DIRECT 策略验证。")
	}
	return report
}

func verifyRoute(ctx context.Context, target netip.Addr, path cfnetwork.PhysicalPath, backend cfnetwork.RouteBackend, dial cfnetwork.DialContextFunc) RouteEvidence {
	evidence := RouteEvidence{Target: target.String()}
	if !target.IsValid() || !target.IsGlobalUnicast() {
		evidence.Error = "target must be a global unicast address"
		return evidence
	}
	resolved, err := backend.Resolve(ctx, target)
	if err != nil {
		evidence.Error = fmt.Sprintf("resolve route: %v", err)
		return evidence
	}
	evidence.Resolved = resolved
	evidence.InterfaceMatches = resolved.Interface == path.Interface || (path.InterfaceIndex > 0 && resolved.InterfaceIndex == path.InterfaceIndex)
	expectedGateway := path.GatewayIPv6
	expectedSources := path.SourceIPv6
	if target.Is4() {
		expectedGateway = path.GatewayIPv4
		expectedSources = path.SourceIPv4
	}
	evidence.GatewayMatches = expectedGateway != "" && resolved.Gateway == expectedGateway
	connection, err := dial(ctx, "tcp", net.JoinHostPort(target.String(), "443"))
	if err != nil {
		evidence.Error = fmt.Sprintf("connect target: %v", err)
		return evidence
	}
	evidence.SocketConnected = true
	if local := connection.LocalAddr(); local != nil {
		host, _, splitErr := net.SplitHostPort(local.String())
		if splitErr == nil {
			evidence.SocketSource = strings.Trim(host, "[]")
		}
	}
	if closeErr := connection.Close(); closeErr != nil {
		evidence.Error = fmt.Sprintf("close diagnostic connection: %v", closeErr)
		return evidence
	}
	for _, source := range expectedSources {
		if evidence.SocketSource == source {
			evidence.SourceMatches = true
			break
		}
	}
	evidence.VerifiedDirect = evidence.SocketConnected && evidence.InterfaceMatches && evidence.GatewayMatches && evidence.SourceMatches
	return evidence
}

func proxyEnvironmentIsSet() bool {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

func findVirtualInterfaces() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var names []string
	for _, iface := range interfaces {
		if cfnetwork.IsLikelyVirtual(iface) {
			names = append(names, iface.Name)
		}
	}
	sort.Strings(names)
	return names
}

func filterProxyProcesses(names []string) []string {
	known := []string{"clash", "mihomo", "sing-box", "singbox", "xray", "v2ray", "wireguard", "openvpn", "surge"}
	seen := map[string]struct{}{}
	var matches []string
	for _, processName := range names {
		lower := strings.ToLower(strings.TrimSpace(processName))
		for _, marker := range known {
			if !strings.Contains(lower, marker) {
				continue
			}
			if _, exists := seen[lower]; !exists {
				seen[lower] = struct{}{}
				matches = append(matches, processName)
			}
			break
		}
	}
	sort.Strings(matches)
	return matches
}
