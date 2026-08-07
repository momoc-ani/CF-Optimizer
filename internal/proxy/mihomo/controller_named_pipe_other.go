//go:build !windows

package mihomo

import (
	"context"
	"errors"
	"net"
)

// namedPipeControllerSupported 表示非 Windows 平台不接受 Named Pipe 控制端。
func namedPipeControllerSupported() bool { return false }

// dialNamedPipeController 防止非 Windows 构建误用 Windows Named Pipe。
func dialNamedPipeController(context.Context, string) (net.Conn, error) {
	return nil, errors.New("Mihomo Named Pipe controllers are only supported on Windows")
}
