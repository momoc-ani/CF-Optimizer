//go:build linux || darwin

package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func detectProxyProcesses(ctx context.Context, timeout time.Duration) ([]string, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "ps", "-eo", "comm=").Output()
	if err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, commandContext.Err()
		}
		return nil, fmt.Errorf("list processes: %w", err)
	}
	return filterProxyProcesses(strings.Split(string(output), "\n")), nil
}
