//go:build windows

package diagnostics

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

func detectProxyProcesses(ctx context.Context, timeout time.Duration) ([]string, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "tasklist.exe", "/FO", "CSV", "/NH").Output()
	if err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, commandContext.Err()
		}
		return nil, fmt.Errorf("list processes: %w", err)
	}
	reader := csv.NewReader(strings.NewReader(string(output)))
	var names []string
	for {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("decode tasklist output: %w", readErr)
		}
		if len(record) > 0 {
			names = append(names, record[0])
		}
	}
	return filterProxyProcesses(names), nil
}
