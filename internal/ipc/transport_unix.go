//go:build linux || darwin

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func listenLocal(endpoint string) (net.Listener, func(), error) {
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o750); err != nil {
		return nil, nil, err
	}
	if fileInfo, err := os.Lstat(endpoint); err == nil {
		if fileInfo.Mode()&os.ModeSocket == 0 {
			return nil, nil, fmt.Errorf("refusing to replace non-socket IPC path %s", endpoint)
		}
		if err := os.Remove(endpoint); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on Unix socket: %w", err)
	}
	if err := os.Chmod(endpoint, 0o660); err != nil {
		_ = listener.Close()
		_ = os.Remove(endpoint)
		return nil, nil, err
	}
	cleanup := func() {
		_ = listener.Close()
		_ = os.Remove(endpoint)
	}
	return listener, cleanup, nil
}

func dialLocal(ctx context.Context, endpoint string) (net.Conn, error) {
	dialer := net.Dialer{}
	return dialer.DialContext(ctx, "unix", endpoint)
}
