//go:build darwin

package network

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// discoverPlatformDNSServers 从 macOS 动态存储中读取目标物理接口的 DNS 配置。
func discoverPlatformDNSServers(ctx context.Context, path PhysicalPath, timeout time.Duration) ([]string, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "scutil", "--dns").CombinedOutput()
	if err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, commandContext.Err()
		}
		return nil, fmt.Errorf("query macOS interface DNS: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return parseDarwinDNSServers(string(output), path.InterfaceIndex), nil
}

// parseDarwinDNSServers 只提取绑定目标接口索引的 nameserver 地址。
func parseDarwinDNSServers(output string, interfaceIndex int) []string {
	requestedIndex := strconv.Itoa(interfaceIndex)
	var result []string
	for _, block := range strings.Split(output, "resolver #") {
		lines := strings.Split(block, "\n")
		matchesInterface := false
		var blockServers []string
		for _, line := range lines {
			key, value, found := strings.Cut(strings.TrimSpace(line), ":")
			if !found {
				continue
			}
			value = strings.TrimSpace(value)
			if key == "if_index" {
				fields := strings.Fields(value)
				matchesInterface = len(fields) > 0 && fields[0] == requestedIndex
			}
			if strings.HasPrefix(key, "nameserver[") {
				blockServers = append(blockServers, value)
			}
		}
		if matchesInterface {
			result = append(result, blockServers...)
		}
	}
	return result
}
