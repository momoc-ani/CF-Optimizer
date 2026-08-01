//go:build linux

package network

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func controlForInterface(name string) (socketControl, error) {
	if name == "" {
		return nil, nil
	}
	if _, err := net.InterfaceByName(name); err != nil {
		return nil, err
	}
	return func(_ string, _ string, connection syscall.RawConn) error {
		var bindErr error
		if err := connection.Control(func(fd uintptr) {
			bindErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, name)
		}); err != nil {
			return err
		}
		return bindErr
	}, nil
}
