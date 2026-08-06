//go:build windows

package mihomo

import (
	"context"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const windowsInternetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// platformSystemProxyState 从全部已加载用户配置读取 Windows 系统代理，兼容服务进程不属于交互用户的情况。
func platformSystemProxyState(_ context.Context, ports []int) (systemProxyState, string) {
	users, err := registry.OpenKey(registry.USERS, ``, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return systemProxyUnknown, "无法读取 Windows 用户代理配置"
	}
	defer users.Close()
	subkeys, err := users.ReadSubKeyNames(-1)
	if err != nil {
		return systemProxyUnknown, "无法枚举 Windows 用户代理配置"
	}
	foundSetting := false
	for _, subkey := range subkeys {
		if strings.HasSuffix(strings.ToLower(subkey), "_classes") {
			continue
		}
		settings, openErr := registry.OpenKey(registry.USERS, subkey+`\`+windowsInternetSettings, registry.QUERY_VALUE)
		if openErr != nil {
			continue
		}
		enabled, _, enabledErr := settings.GetIntegerValue("ProxyEnable")
		server, _, serverErr := settings.GetStringValue("ProxyServer")
		settings.Close()
		if enabledErr != nil {
			continue
		}
		foundSetting = true
		if enabled != 0 && serverErr == nil && proxySettingUsesPorts(server, ports) {
			return systemProxyOn, "Windows 系统代理指向 Mihomo"
		}
	}
	if foundSetting {
		return systemProxyOff, "Windows 系统代理未指向 Mihomo"
	}
	return systemProxyUnknown, "未找到已加载用户的 Windows 系统代理配置"
}
