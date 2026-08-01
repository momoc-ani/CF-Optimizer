//go:build darwin

package network

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func discoverPlatformPath(ctx context.Context, interfaceOverride string, timeout time.Duration) (PhysicalPath, error) {
	path := PhysicalPath{Interface: interfaceOverride}
	v4, v4Err := readDarwinDefault(ctx, timeout, false)
	v6, v6Err := readDarwinDefault(ctx, timeout, true)
	if v4Err != nil && v6Err != nil {
		return PhysicalPath{}, fmt.Errorf("read macOS default routes: IPv4: %v; IPv6: %v", v4Err, v6Err)
	}
	if path.Interface == "" {
		path.Interface = v4.Interface
		if path.Interface == "" {
			path.Interface = v6.Interface
		}
	}
	if v4.Interface == path.Interface {
		path.GatewayIPv4 = v4.GatewayIPv4
	}
	if v6.Interface == path.Interface {
		path.GatewayIPv6 = v6.GatewayIPv6
	}
	return path, nil
}

func readDarwinDefault(ctx context.Context, timeout time.Duration, ipv6 bool) (PhysicalPath, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	arguments := []string{"-n", "get", "default"}
	if ipv6 {
		arguments = []string{"-n", "get", "-inet6", "default"}
	}
	output, err := exec.CommandContext(commandContext, "route", arguments...).CombinedOutput()
	if err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return PhysicalPath{}, commandContext.Err()
		}
		return PhysicalPath{}, fmt.Errorf("route get default: %w: %s", err, strings.TrimSpace(string(output)))
	}
	fields := parseDarwinRouteFields(string(output))
	path := PhysicalPath{Interface: fields["interface"]}
	if ipv6 {
		path.GatewayIPv6 = fields["gateway"]
	} else {
		path.GatewayIPv4 = fields["gateway"]
	}
	return path, nil
}
