//go:build !windows && !linux && !darwin

package network

import (
	"context"
	"errors"
	"time"
)

func discoverPlatformDNSServers(context.Context, PhysicalPath, time.Duration) ([]string, error) {
	return nil, errors.New("physical DNS discovery is unsupported on this platform")
}
