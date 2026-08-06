//go:build darwin

package mihomo

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// platformSystemProxyState 使用系统 Dynamic Store 读取 macOS 当前网络服务代理状态。
func platformSystemProxyState(ctx context.Context, ports []int) (systemProxyState, string) {
	output, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return systemProxyUnknown, fmt.Sprintf("读取 macOS 系统代理失败: %v", err)
	}
	values := parseColonSettings(string(output))
	for _, prefix := range []string{"HTTP", "HTTPS", "SOCKS"} {
		if values[prefix+"Enable"] != "1" || !isLoopbackProxyHost(values[prefix+"Proxy"]) {
			continue
		}
		port, parseErr := strconv.Atoi(values[prefix+"Port"])
		if parseErr == nil && containsPort(ports, port) {
			return systemProxyOn, "macOS 系统代理指向 Mihomo"
		}
	}
	return systemProxyOff, "macOS 系统代理未指向 Mihomo"
}

func parseColonSettings(raw string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if found {
			result[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return result
}
