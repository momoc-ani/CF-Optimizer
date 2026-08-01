//go:build !windows

package servicehost

import "context"

// Run 在非 Windows 平台直接执行由 systemd 或 launchd 监管的服务函数。
func Run(ctx context.Context, service func(context.Context) error) error {
	return service(ctx)
}
