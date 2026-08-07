//go:build windows

package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

type windowsListenerRecord struct {
	LocalAddress string `json:"LocalAddress"`
	LocalPort    int    `json:"LocalPort"`
	ProcessName  string `json:"ProcessName"`
}

type windowsProcessRecord struct {
	ProcessName string `json:"ProcessName"`
	CommandLine string `json:"CommandLine"`
}

type windowsDiscoverySnapshot struct {
	Processes []windowsProcessRecord  `json:"Processes"`
	Listeners []windowsListenerRecord `json:"Listeners"`
}

// platformControllerCandidates 兼容发现 Windows Mihomo/Clash 的 Named Pipe 与 TCP 控制端。
func platformControllerCandidates(ctx context.Context) ([]controllerCandidate, error) {
	const script = `[Console]::OutputEncoding=[Text.UTF8Encoding]::new(); [array]$processes=Get-CimInstance Win32_Process -Filter "Name LIKE '%mihomo%' OR Name LIKE '%clash%'" -ErrorAction SilentlyContinue|ForEach-Object{[PSCustomObject]@{ProcessName=$_.Name;CommandLine=$_.CommandLine}}; [array]$listeners=Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue|ForEach-Object{$p=Get-Process -Id $_.OwningProcess -ErrorAction SilentlyContinue;if($p -and $p.ProcessName -match '(?i)(mihomo|clash)'){[PSCustomObject]@{LocalAddress=$_.LocalAddress;LocalPort=$_.LocalPort;ProcessName=$p.ProcessName}}}; [PSCustomObject]@{Processes=$processes;Listeners=$listeners}|ConvertTo-Json -Depth 4 -Compress`
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("PowerShell 监听端口查询失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	candidates, err := decodeWindowsControllerCandidates(output)
	if err != nil {
		return nil, err
	}
	return append(candidates, windowsActiveConfigControllerCandidates()...), nil
}

// decodeWindowsControllerCandidates 将 Windows 进程与监听快照限制为本机安全控制端。
func decodeWindowsControllerCandidates(output []byte) ([]controllerCandidate, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}
	var snapshot windowsDiscoverySnapshot
	if err := json.Unmarshal(output, &snapshot); err != nil {
		return nil, fmt.Errorf("解析 Windows 监听端口结果: %w", err)
	}
	var candidates []controllerCandidate
	for _, process := range snapshot.Processes {
		if candidate, ok := windowsProcessControllerCandidate(process); ok {
			candidates = append(candidates, candidate)
		}
	}
	for _, record := range snapshot.Listeners {
		if record.LocalPort < 1 || record.LocalPort > 65535 || !isMihomoProcess(record.ProcessName) {
			continue
		}
		host := ""
		switch record.LocalAddress {
		case "127.0.0.1", "0.0.0.0":
			host = "127.0.0.1"
		case "::1", "::":
			host = "[::1]"
		default:
			continue
		}
		candidates = append(candidates, controllerCandidate{Controller: fmt.Sprintf("http://%s:%d", host, record.LocalPort), Process: record.ProcessName})
	}
	return candidates, nil
}

// windowsProcessControllerCandidate 从 Mihomo 启动参数提取新版 Named Pipe 及其活动配置路径。
func windowsProcessControllerCandidate(record windowsProcessRecord) (controllerCandidate, bool) {
	if !isMihomoProcess(record.ProcessName) || strings.TrimSpace(record.CommandLine) == "" {
		return controllerCandidate{}, false
	}
	arguments, err := windows.DecomposeCommandLine(record.CommandLine)
	if err != nil {
		return controllerCandidate{}, false
	}
	pipePath := windowsCommandLineFlag(arguments, "-ext-ctl-pipe", "--ext-ctl-pipe", "-external-controller-pipe", "--external-controller-pipe")
	controller, err := namedPipeControllerEndpoint(pipePath)
	if err != nil {
		return controllerCandidate{}, false
	}
	configPath := windowsCommandLineFlag(arguments, "-f", "--config")
	dataDir := windowsCommandLineFlag(arguments, "-d", "--directory")
	if configPath != "" && !filepath.IsAbs(configPath) && filepath.IsAbs(dataDir) {
		configPath = filepath.Join(dataDir, configPath)
	}
	if !filepath.IsAbs(configPath) {
		configPath = ""
	} else {
		configPath = filepath.Clean(configPath)
	}
	processName := strings.TrimSuffix(record.ProcessName, filepath.Ext(record.ProcessName))
	return controllerCandidate{Controller: controller, Process: processName, ConfigPath: configPath}, true
}

// windowsCommandLineFlag 兼容 Windows 命令行中的“参数 值”和“参数=值”两种形式，并采用最后一次声明。
func windowsCommandLineFlag(arguments []string, names ...string) string {
	value := ""
	for index, argument := range arguments {
		for _, name := range names {
			if strings.EqualFold(argument, name) && index+1 < len(arguments) {
				value = strings.TrimSpace(arguments[index+1])
				continue
			}
			prefix := name + "="
			if len(argument) > len(prefix) && strings.EqualFold(argument[:len(prefix)], prefix) {
				value = strings.TrimSpace(argument[len(prefix):])
			}
		}
	}
	return value
}

// windowsActiveConfigControllerCandidates 从已知客户端配置回退发现未暴露在命令行中的 Named Pipe。
func windowsActiveConfigControllerCandidates() []controllerCandidate {
	var candidates []controllerCandidate
	for _, configPath := range activeConfigCandidates() {
		content, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}
		controller, ok := activeConfigNamedPipeController(content)
		if !ok {
			continue
		}
		candidates = append(candidates, controllerCandidate{Controller: controller, ConfigPath: configPath})
	}
	return candidates
}
