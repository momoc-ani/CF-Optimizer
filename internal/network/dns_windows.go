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

// discoverPlatformDNSServers 通过 PowerShell 读取目标 Windows 接口索引的 DNS 地址。
func discoverPlatformDNSServers(ctx context.Context, path PhysicalPath, timeout time.Duration) ([]string, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	script := fmt.Sprintf("[Console]::OutputEncoding=[Text.UTF8Encoding]::new(); [array]$servers=(Get-DnsClientServerAddress -InterfaceIndex %d -ErrorAction Stop).ServerAddresses; ConvertTo-Json -InputObject $servers -Compress", path.InterfaceIndex)
	output, err := exec.CommandContext(commandContext, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, commandContext.Err()
		}
		return nil, fmt.Errorf("query Windows interface DNS: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var servers []string
	if err := json.Unmarshal(output, &servers); err != nil {
		return nil, fmt.Errorf("decode Windows interface DNS: %w", err)
	}
	return servers, nil
}
