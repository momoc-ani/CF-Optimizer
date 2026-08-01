//go:build !linux && !darwin && !windows

package diagnostics

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

func detectProxyProcesses(context.Context, time.Duration) ([]string, error) {
	return nil, fmt.Errorf("process detection is not supported on %s", runtime.GOOS)
}
