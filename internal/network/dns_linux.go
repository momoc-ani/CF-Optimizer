//go:build linux

package network

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// discoverPlatformDNSServers 优先读取 systemd-resolved 的接口 DNS，并回退到 resolv.conf。
func discoverPlatformDNSServers(ctx context.Context, path PhysicalPath, timeout time.Duration) ([]string, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, commandErr := exec.CommandContext(commandContext, "resolvectl", "dns", path.Interface).CombinedOutput()
	if commandErr == nil {
		if separator := strings.IndexByte(string(output), ':'); separator >= 0 {
			return strings.Fields(string(output)[separator+1:]), nil
		}
	}
	servers, fileErr := readResolvConf("/run/systemd/resolve/resolv.conf")
	if fileErr != nil || len(servers) == 0 {
		servers, fileErr = readResolvConf("/etc/resolv.conf")
	}
	if len(servers) > 0 {
		return servers, nil
	}
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
		commandErr = commandContext.Err()
	}
	return nil, fmt.Errorf("discover Linux interface DNS: resolvectl: %v; resolv.conf: %v", commandErr, fileErr)
}
