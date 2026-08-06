//go:build !windows && !linux && !darwin

package mihomo

import "context"

func platformSystemProxyState(_ context.Context, _ []int) (systemProxyState, string) {
	return systemProxyUnknown, "当前平台不支持系统代理状态检测"
}
