//go:build darwin

package network

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const darwinResolvConfPath = "/etc/resolv.conf"

type darwinDNSQuery func(context.Context, time.Duration) ([]byte, error)
type resolvConfReader func(string) ([]string, error)

// discoverPlatformDNSServers 优先读取 macOS 动态存储，并在接口 resolver 缺失时回退 resolv.conf。
func discoverPlatformDNSServers(ctx context.Context, path PhysicalPath, timeout time.Duration) ([]string, error) {
	return discoverDarwinDNSServers(ctx, path, timeout, queryDarwinDNS, readResolvConf)
}

// discoverDarwinDNSServers 按动态存储、resolv.conf 的顺序发现物理接口 DNS。
func discoverDarwinDNSServers(ctx context.Context, path PhysicalPath, timeout time.Duration, query darwinDNSQuery, readConfig resolvConfReader) ([]string, error) {
	output, commandErr := query(ctx, timeout)
	if commandErr == nil {
		servers := normalizeDNSServers(parseDarwinDNSServers(string(output), path.InterfaceIndex))
		if len(servers) > 0 {
			return servers, nil
		}
		commandErr = fmt.Errorf("scutil returned no resolver for interface index %d", path.InterfaceIndex)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	servers, fileErr := readConfig(darwinResolvConfPath)
	servers = normalizeDNSServers(servers)
	if len(servers) > 0 {
		return servers, nil
	}
	return nil, fmt.Errorf("discover macOS interface DNS: scutil: %v; %s: %v", commandErr, darwinResolvConfPath, fileErr)
}

// queryDarwinDNS 在受限时间内读取 macOS 动态 DNS 配置。
func queryDarwinDNS(ctx context.Context, timeout time.Duration) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "scutil", "--dns").CombinedOutput()
	if err != nil {
		if commandContext.Err() != nil {
			return nil, commandContext.Err()
		}
		return nil, fmt.Errorf("query macOS interface DNS: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
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
			key = strings.TrimSpace(key)
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
