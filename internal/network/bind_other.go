//go:build !linux && !darwin && !windows

package network

import (
	"fmt"
	"runtime"
)

func controlForInterface(name string) (socketControl, error) {
	if name != "" {
		return nil, fmt.Errorf("interface binding is not supported on %s", runtime.GOOS)
	}
	return nil, nil
}
