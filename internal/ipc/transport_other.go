//go:build !linux && !darwin && !windows

package ipc

import (
	"context"
	"fmt"
	"net"
	"runtime"
)

func listenLocal(string) (net.Listener, func(), error) {
	return nil, nil, fmt.Errorf("local IPC is not supported on %s", runtime.GOOS)
}

func dialLocal(context.Context, string) (net.Conn, error) {
	return nil, fmt.Errorf("local IPC is not supported on %s", runtime.GOOS)
}
