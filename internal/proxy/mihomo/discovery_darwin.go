//go:build darwin

package mihomo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// platformControllerCandidates 使用 macOS 自带 lsof 读取 Mihomo/Clash TCP 和 Unix Socket 控制端。
func platformControllerCandidates(ctx context.Context) ([]controllerCandidate, error) {
	output, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-U", "-Fpcn").CombinedOutput()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && len(output) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("lsof 监听端口查询失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return parseDarwinControllerCandidates(string(output)), nil
}

// parseDarwinControllerCandidates 关联 lsof 的进程与地址记录并限制为本机回环访问。
func parseDarwinControllerCandidates(output string) []controllerCandidate {
	processName := ""
	var candidates []controllerCandidate
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'c':
			processName = strings.TrimSpace(line[1:])
		case 'n':
			if !isMihomoProcess(processName) {
				continue
			}
			controller, ok := darwinControllerEndpoint(processName, strings.TrimSpace(line[1:]))
			if ok {
				candidates = append(candidates, controllerCandidate{Controller: controller, Process: processName})
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftUnix := strings.HasPrefix(candidates[i].Controller, unixControllerScheme+"://")
		rightUnix := strings.HasPrefix(candidates[j].Controller, unixControllerScheme+"://")
		return leftUnix && !rightUnix
	})
	return candidates
}

// darwinControllerEndpoint 将 lsof 地址记录转换为受限的本机控制端。
func darwinControllerEndpoint(processName, listener string) (string, bool) {
	if filepath.IsAbs(listener) {
		if !isDarwinUnixControllerProcess(processName, listener) {
			return "", false
		}
		return (&url.URL{Scheme: unixControllerScheme, Path: filepath.Clean(listener)}).String(), true
	}
	return darwinControllerURL(listener)
}

// isDarwinUnixControllerProcess 排除 Clash UI 持有的无关 Socket，仅保留内核或控制端命名 Socket。
func isDarwinUnixControllerProcess(processName, listener string) bool {
	process := strings.ToLower(strings.TrimSpace(processName))
	socketName := strings.ToLower(filepath.Base(listener))
	return strings.Contains(process, "mihomo") || process == "clash" || strings.HasPrefix(process, "clash-meta") || strings.Contains(socketName, "mihomo") || strings.Contains(socketName, "clash")
}

// darwinControllerURL 将 lsof 监听地址转换为安全的回环 HTTP 端点。
func darwinControllerURL(listener string) (string, bool) {
	separator := strings.LastIndex(listener, ":")
	if separator < 0 {
		return "", false
	}
	port, err := strconv.Atoi(listener[separator+1:])
	if err != nil || port < 1 || port > 65535 {
		return "", false
	}
	host := listener[:separator]
	switch host {
	case "127.0.0.1", "localhost", "*":
		return fmt.Sprintf("http://127.0.0.1:%d", port), true
	case "[::1]", "::1", "[::]", "::":
		return fmt.Sprintf("http://[::1]:%d", port), true
	default:
		return "", false
	}
}
