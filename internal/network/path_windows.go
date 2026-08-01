//go:build windows

package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type windowsDefaultRoute struct {
	NextHop        string `json:"NextHop"`
	InterfaceAlias string `json:"InterfaceAlias"`
}

func discoverPlatformPath(ctx context.Context, interfaceOverride string, timeout time.Duration) (PhysicalPath, error) {
	path := PhysicalPath{Interface: interfaceOverride}
	v4, v4Err := readWindowsDefault(ctx, timeout, "IPv4")
	v6, v6Err := readWindowsDefault(ctx, timeout, "IPv6")
	if v4Err != nil && v6Err != nil {
		return PhysicalPath{}, fmt.Errorf("read Windows default routes: IPv4: %v; IPv6: %v", v4Err, v6Err)
	}
	if path.Interface == "" {
		path.Interface = v4.InterfaceAlias
		if path.Interface == "" {
			path.Interface = v6.InterfaceAlias
		}
	}
	if v4.InterfaceAlias == path.Interface {
		path.GatewayIPv4 = v4.NextHop
	}
	if v6.InterfaceAlias == path.Interface {
		path.GatewayIPv6 = v6.NextHop
	}
	return path, nil
}

func readWindowsDefault(ctx context.Context, timeout time.Duration, family string) (windowsDefaultRoute, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	prefix := "0.0.0.0/0"
	if family == "IPv6" {
		prefix = "::/0"
	}
	script := fmt.Sprintf("[Console]::OutputEncoding=[Text.UTF8Encoding]::new(); Get-NetRoute -AddressFamily %s -DestinationPrefix '%s'|Sort-Object RouteMetric|Select-Object -First 1 NextHop,InterfaceAlias|ConvertTo-Json -Compress", family, prefix)
	output, err := exec.CommandContext(commandContext, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return windowsDefaultRoute{}, commandContext.Err()
		}
		return windowsDefaultRoute{}, fmt.Errorf("query Windows default route: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var route windowsDefaultRoute
	if err := json.Unmarshal(output, &route); err != nil {
		return route, fmt.Errorf("decode Windows default route: %w", err)
	}
	return route, nil
}
