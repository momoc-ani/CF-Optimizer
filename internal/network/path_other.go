//go:build !linux && !darwin && !windows

package network

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

func discoverPlatformPath(context.Context, string, time.Duration) (PhysicalPath, error) {
	return PhysicalPath{}, fmt.Errorf("physical path discovery is not supported on %s", runtime.GOOS)
}
