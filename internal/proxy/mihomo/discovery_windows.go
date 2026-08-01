//go:build windows

package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type windowsListenerRecord struct {
	LocalAddress string `json:"LocalAddress"`
	LocalPort    int    `json:"LocalPort"`
	ProcessName  string `json:"ProcessName"`
}

// platformControllerCandidates 通过 Windows TCP 监听表定位 Mihomo/Clash 进程端口。
func platformControllerCandidates(ctx context.Context) ([]controllerCandidate, error) {
	const script = `[Console]::OutputEncoding=[Text.UTF8Encoding]::new(); [array]$items=Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue|ForEach-Object{$p=Get-Process -Id $_.OwningProcess -ErrorAction SilentlyContinue;if($p -and $p.ProcessName -match '(?i)(mihomo|clash)'){[PSCustomObject]@{LocalAddress=$_.LocalAddress;LocalPort=$_.LocalPort;ProcessName=$p.ProcessName}}}; ConvertTo-Json -InputObject $items -Compress`
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("PowerShell 监听端口查询失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return decodeWindowsControllerCandidates(output)
}

// decodeWindowsControllerCandidates 将 Windows 监听记录限制为可安全访问的回环端点。
func decodeWindowsControllerCandidates(output []byte) ([]controllerCandidate, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}
	var records []windowsListenerRecord
	if err := json.Unmarshal(output, &records); err != nil {
		return nil, fmt.Errorf("解析 Windows 监听端口结果: %w", err)
	}
	var candidates []controllerCandidate
	for _, record := range records {
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
