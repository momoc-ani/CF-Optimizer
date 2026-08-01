//go:build darwin

package network

import (
	"net"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func controlForInterface(name string) (socketControl, error) {
	if name == "" {
		return nil, nil
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	return func(network string, _ string, connection syscall.RawConn) error {
		var bindErr error
		if err := connection.Control(func(fd uintptr) {
			if strings.HasSuffix(network, "6") {
				bindErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, iface.Index)
			} else {
				bindErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, iface.Index)
			}
		}); err != nil {
			return err
		}
		return bindErr
	}, nil
}
