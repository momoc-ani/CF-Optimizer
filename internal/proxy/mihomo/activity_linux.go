//go:build linux

package mihomo

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
)

// platformSystemProxyState 依次检查 GNOME 与 KDE 的当前桌面代理配置。
func platformSystemProxyState(ctx context.Context, ports []int) (systemProxyState, string) {
	gnomeState, gnomeMessage := gnomeSystemProxyState(ctx, ports)
	if gnomeState != systemProxyUnknown {
		return gnomeState, gnomeMessage
	}
	kdeState, kdeMessage := kdeSystemProxyState(ctx, ports)
	if kdeState != systemProxyUnknown {
		return kdeState, kdeMessage
	}
	return systemProxyUnknown, "无法读取 GNOME 或 KDE 当前用户代理配置"
}

func gnomeSystemProxyState(ctx context.Context, ports []int) (systemProxyState, string) {
	observed := systemProxyUnknown
	for _, uid := range linuxDesktopSessionUIDs() {
		modeOutput, err := runLinuxDesktopCommand(ctx, uid, "gsettings", "get", "org.gnome.system.proxy", "mode")
		if err != nil {
			continue
		}
		mode := strings.Trim(strings.TrimSpace(string(modeOutput)), "'\"")
		if mode == "none" {
			observed = systemProxyOff
			continue
		}
		if mode != "manual" {
			continue
		}
		observed = systemProxyOff
		for _, protocol := range []string{"http", "https", "socks"} {
			hostOutput, hostErr := runLinuxDesktopCommand(ctx, uid, "gsettings", "get", "org.gnome.system.proxy."+protocol, "host")
			portOutput, portErr := runLinuxDesktopCommand(ctx, uid, "gsettings", "get", "org.gnome.system.proxy."+protocol, "port")
			if hostErr != nil || portErr != nil {
				continue
			}
			host := strings.Trim(strings.TrimSpace(string(hostOutput)), "'\"")
			port, parseErr := strconv.Atoi(strings.TrimSpace(string(portOutput)))
			if parseErr == nil && isLoopbackProxyHost(host) && containsPort(ports, port) {
				return systemProxyOn, "GNOME 系统代理指向 Mihomo"
			}
		}
	}
	if observed == systemProxyOff {
		return observed, "GNOME 系统代理未指向 Mihomo"
	}
	return systemProxyUnknown, ""
}

func kdeSystemProxyState(ctx context.Context, ports []int) (systemProxyState, string) {
	observed := systemProxyUnknown
	for _, uid := range linuxDesktopSessionUIDs() {
		for _, executable := range []string{"kreadconfig6", "kreadconfig5"} {
			proxyType, err := runLinuxDesktopCommand(ctx, uid, executable, "--file", "kioslaverc", "--group", "Proxy Settings", "--key", "ProxyType")
			if err != nil {
				continue
			}
			value := strings.TrimSpace(string(proxyType))
			if value == "0" || value == "" {
				observed = systemProxyOff
				break
			}
			if value != "1" {
				break
			}
			observed = systemProxyOff
			for _, key := range []string{"httpProxy", "httpsProxy", "socksProxy"} {
				output, readErr := runLinuxDesktopCommand(ctx, uid, executable, "--file", "kioslaverc", "--group", "Proxy Settings", "--key", key)
				if readErr == nil && proxySettingUsesPorts(strings.TrimSpace(string(output)), ports) {
					return systemProxyOn, "KDE 系统代理指向 Mihomo"
				}
			}
			break
		}
	}
	if observed == systemProxyOff {
		return observed, "KDE 系统代理未指向 Mihomo"
	}
	return systemProxyUnknown, "KDE 代理配置工具不可用"
}

// linuxDesktopSessionUIDs 从已建立运行时目录的桌面会话中选择需要读取的用户。
func linuxDesktopSessionUIDs() []int {
	seen := map[int]struct{}{}
	result := []int{}
	entries, err := os.ReadDir("/run/user")
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			uid, parseErr := strconv.Atoi(entry.Name())
			if parseErr != nil || uid < 0 {
				continue
			}
			seen[uid] = struct{}{}
			result = append(result, uid)
		}
	}
	current := os.Geteuid()
	if _, exists := seen[current]; !exists {
		result = append(result, current)
	}
	return result
}

// runLinuxDesktopCommand 在对应桌面用户的 D-Bus 会话中执行只读代理查询。
func runLinuxDesktopCommand(ctx context.Context, uid int, executable string, arguments ...string) ([]byte, error) {
	runtimeDirectory := "/run/user/" + strconv.Itoa(uid)
	if uid == os.Geteuid() {
		command := exec.CommandContext(ctx, executable, arguments...)
		command.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runtimeDirectory, "DBUS_SESSION_BUS_ADDRESS=unix:path="+runtimeDirectory+"/bus")
		return command.Output()
	}
	account, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return nil, err
	}
	commandArguments := []string{"-u", account.Username, "--", "env", "XDG_RUNTIME_DIR=" + runtimeDirectory, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtimeDirectory + "/bus", executable}
	commandArguments = append(commandArguments, arguments...)
	return exec.CommandContext(ctx, "runuser", commandArguments...).Output()
}
