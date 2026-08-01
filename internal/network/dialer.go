package network

import (
	"context"
	"net"
	"syscall"
	"time"
)

// DialContextFunc 与 net.Dialer.DialContext 兼容，便于注入平台绑定或测试替身。
type DialContextFunc func(context.Context, string, string) (net.Conn, error)

// NewBoundDialer 创建禁用代理并可绑定物理接口的原始 Socket Dialer。
func NewBoundDialer(interfaceName string, timeout time.Duration) (DialContextFunc, error) {
	control, err := controlForInterface(interfaceName)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: -1, Control: control}
	return dialer.DialContext, nil
}

type socketControl func(network, address string, connection syscall.RawConn) error
