package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// PhysicalPath 描述排除已知虚拟接口后选出的默认物理出口。
type PhysicalPath struct {
	Interface      string   `json:"interface"`
	InterfaceIndex int      `json:"interface_index"`
	GatewayIPv4    string   `json:"gateway_ipv4,omitempty"`
	GatewayIPv6    string   `json:"gateway_ipv6,omitempty"`
	SourceIPv4     []string `json:"source_ipv4,omitempty"`
	SourceIPv6     []string `json:"source_ipv6,omitempty"`
	IsOverride     bool     `json:"is_override"`
}

// DiscoverPhysicalPath 查询平台默认出口，并应用经过校验的接口和网关覆盖值。
func DiscoverPhysicalPath(ctx context.Context, interfaceOverride, gatewayIPv4, gatewayIPv6 string, timeout time.Duration) (PhysicalPath, error) {
	path, err := discoverPlatformPath(ctx, interfaceOverride, timeout)
	if err != nil {
		return PhysicalPath{}, err
	}
	if interfaceOverride != "" {
		path.IsOverride = true
	}
	if gatewayIPv4 != "" {
		path.GatewayIPv4 = gatewayIPv4
		path.IsOverride = true
	}
	if gatewayIPv6 != "" {
		path.GatewayIPv6 = gatewayIPv6
		path.IsOverride = true
	}
	if path.Interface == "" {
		return PhysicalPath{}, fmt.Errorf("no physical default interface was found")
	}
	iface, err := net.InterfaceByName(path.Interface)
	if err != nil {
		return PhysicalPath{}, fmt.Errorf("resolve physical interface %q: %w", path.Interface, err)
	}
	if IsLikelyVirtual(*iface) && interfaceOverride == "" {
		return PhysicalPath{}, fmt.Errorf("selected default interface %q is virtual", path.Interface)
	}
	path.InterfaceIndex = iface.Index
	addresses, err := iface.Addrs()
	if err != nil {
		return PhysicalPath{}, fmt.Errorf("read interface addresses: %w", err)
	}
	for _, rawAddress := range addresses {
		address, _, err := net.ParseCIDR(rawAddress.String())
		if err != nil || address == nil || address.IsLoopback() || address.IsLinkLocalUnicast() {
			continue
		}
		if address.To4() != nil {
			path.SourceIPv4 = append(path.SourceIPv4, address.String())
		} else {
			path.SourceIPv6 = append(path.SourceIPv6, address.String())
		}
	}
	sort.Strings(path.SourceIPv4)
	sort.Strings(path.SourceIPv6)
	return path, nil
}

// IsLikelyVirtual 识别常见 TUN、VPN、容器和虚拟机接口名称。
func IsLikelyVirtual(iface net.Interface) bool {
	if iface.Flags&net.FlagLoopback != 0 {
		return true
	}
	name := strings.ToLower(iface.Name)
	virtualMarkers := []string{
		"tun", "tap", "wintun", "utun", "wg", "wireguard", "tailscale", "zerotier",
		"docker", "br-", "veth", "virbr", "vmnet", "vbox", "hyper-v", "vethernet",
	}
	for _, marker := range virtualMarkers {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// NetworkFingerprint 返回默认路径和活动接口集合的稳定摘要，供网络变化调度使用。
func NetworkFingerprint(ctx context.Context, timeout time.Duration) (string, error) {
	path, pathErr := discoverPlatformPath(ctx, "", timeout)
	interfaces, interfaceErr := net.Interfaces()
	if pathErr != nil && interfaceErr != nil {
		return "", fmt.Errorf("discover network fingerprint: %w; interfaces: %v", pathErr, interfaceErr)
	}
	parts := []string{path.Interface, path.GatewayIPv4, path.GatewayIPv6}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 {
			parts = append(parts, fmt.Sprintf("%s:%d:%s", iface.Name, iface.Index, iface.HardwareAddr))
		}
	}
	sort.Strings(parts)
	digest := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(digest[:]), nil
}
