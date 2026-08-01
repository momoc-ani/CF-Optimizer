//go:build linux

package mihomo

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// platformControllerCandidates 从 procfs 关联 Mihomo/Clash 进程套接字与监听端口。
func platformControllerCandidates(_ context.Context) ([]controllerCandidate, error) {
	socketProcesses := linuxMihomoSocketProcesses()
	if len(socketProcesses) == 0 {
		return nil, nil
	}
	var candidates []controllerCandidate
	for _, table := range []struct {
		path string
		ipv6 bool
	}{{"/proc/net/tcp", false}, {"/proc/net/tcp6", true}} {
		listeners, err := parseLinuxTCPListeners(table.path, table.ipv6, socketProcesses)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		candidates = append(candidates, listeners...)
	}
	return candidates, nil
}

// linuxMihomoSocketProcesses 收集目标进程持有的 socket inode，忽略无权限读取的其他进程。
func linuxMihomoSocketProcesses() map[string]string {
	result := map[string]string{}
	processDirectories, _ := filepath.Glob("/proc/[0-9]*")
	for _, processDirectory := range processDirectories {
		nameBytes, err := os.ReadFile(filepath.Join(processDirectory, "comm"))
		if err != nil || !isMihomoProcess(string(nameBytes)) {
			continue
		}
		processName := strings.TrimSpace(string(nameBytes))
		fileDescriptors, err := os.ReadDir(filepath.Join(processDirectory, "fd"))
		if err != nil {
			continue
		}
		for _, descriptor := range fileDescriptors {
			target, err := os.Readlink(filepath.Join(processDirectory, "fd", descriptor.Name()))
			if err == nil && strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
				result[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = processName
			}
		}
	}
	return result
}

// parseLinuxTCPListeners 解析 procfs TCP 表，并仅返回回环或通配监听地址。
func parseLinuxTCPListeners(path string, ipv6 bool, socketProcesses map[string]string) ([]controllerCandidate, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var candidates []controllerCandidate
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		processName, exists := socketProcesses[fields[9]]
		if !exists {
			continue
		}
		addressAndPort := strings.Split(fields[1], ":")
		if len(addressAndPort) != 2 || !isLinuxLoopbackListener(addressAndPort[0], ipv6) {
			continue
		}
		port, err := strconv.ParseUint(addressAndPort[1], 16, 16)
		if err != nil || port == 0 {
			continue
		}
		host := "127.0.0.1"
		if ipv6 {
			host = "[::1]"
		}
		candidates = append(candidates, controllerCandidate{Controller: fmt.Sprintf("http://%s:%d", host, port), Process: processName})
	}
	return candidates, scanner.Err()
}

// isLinuxLoopbackListener 识别 procfs 中的 IPv4/IPv6 回环与通配地址编码。
func isLinuxLoopbackListener(encoded string, ipv6 bool) bool {
	if ipv6 {
		return encoded == strings.Repeat("0", 32) || encoded == "00000000000000000000000001000000"
	}
	return encoded == "00000000" || encoded == "0100007F"
}
