//go:build windows

package mihomo

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// namedPipeControllerSupported 表示当前平台可通过 Windows Named Pipe 访问 Mihomo 控制面。
func namedPipeControllerSupported() bool { return true }

// dialNamedPipeController 连接本机 Mihomo Named Pipe，并由请求上下文控制取消。
func dialNamedPipeController(ctx context.Context, pipePath string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, pipePath)
}
