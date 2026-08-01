//go:build windows

package network

import (
	"math/bits"
	"net"
	"strings"
	"syscall"
)

const ipUnicastInterface = 31

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
			value := iface.Index
			level := syscall.IPPROTO_IPV6
			if !strings.HasSuffix(network, "6") {
				level = syscall.IPPROTO_IP
				value = int(bits.ReverseBytes32(uint32(value)))
			}
			bindErr = syscall.SetsockoptInt(syscall.Handle(fd), level, ipUnicastInterface, value)
		}); err != nil {
			return err
		}
		return bindErr
	}, nil
}
