//go:build darwin

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDarwinStartSucceedsWhenKickstartCompletes(t *testing.T) {
	callCount := 0
	controller := newDarwinTestController(func(_ context.Context, arguments ...string) ([]byte, error) {
		callCount++
		if arguments[0] != "kickstart" {
			t.Fatalf("unexpected launchctl command: %v", arguments)
		}
		return nil, nil
	})

	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if callCount != 1 {
		t.Fatalf("launchctl call count = %d, want 1", callCount)
	}
}

func TestDarwinStartAcceptsTimeoutWhenServiceIsRunning(t *testing.T) {
	callCount := 0
	controller := newDarwinTestController(func(_ context.Context, arguments ...string) ([]byte, error) {
		callCount++
		switch arguments[0] {
		case "kickstart":
			return nil, context.DeadlineExceeded
		case "print":
			return []byte("state = running\n"), nil
		default:
			t.Fatalf("unexpected launchctl command: %v", arguments)
			return nil, nil
		}
	})

	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if callCount != 2 {
		t.Fatalf("launchctl call count = %d, want 2", callCount)
	}
}

func TestDarwinStartWaitsForDelayedLaunchdTransition(t *testing.T) {
	printCalls := 0
	controller := newDarwinTestController(func(_ context.Context, arguments ...string) ([]byte, error) {
		switch arguments[0] {
		case "kickstart":
			return nil, context.DeadlineExceeded
		case "print":
			printCalls++
			if printCalls == 1 {
				return []byte("state = waiting\n"), nil
			}
			return []byte("state = running\n"), nil
		default:
			t.Fatalf("unexpected launchctl command: %v", arguments)
			return nil, nil
		}
	})

	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if printCalls != 2 {
		t.Fatalf("launchctl print call count = %d, want 2", printCalls)
	}
}

func TestDarwinStartRejectsTimeoutWhenServiceIsNotRunning(t *testing.T) {
	printCalls := 0
	controller := newDarwinTestController(func(_ context.Context, arguments ...string) ([]byte, error) {
		switch arguments[0] {
		case "kickstart":
			return nil, context.DeadlineExceeded
		case "print":
			printCalls++
			return []byte("state = waiting\n"), nil
		default:
			t.Fatalf("unexpected launchctl command: %v", arguments)
			return nil, nil
		}
	})

	err := controller.Start(context.Background())
	if err == nil {
		t.Fatal("Start() error = nil, want timeout failure")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start() error = %v, want context deadline exceeded", err)
	}
	if printCalls < 2 {
		t.Fatalf("launchctl print call count = %d, want polling before failure", printCalls)
	}
}

func TestDarwinStartReportsRepeatedStateQueryFailure(t *testing.T) {
	wantErr := errors.New("launchd unavailable")
	printCalls := 0
	controller := newDarwinTestController(func(_ context.Context, arguments ...string) ([]byte, error) {
		switch arguments[0] {
		case "kickstart":
			return nil, context.DeadlineExceeded
		case "print":
			printCalls++
			return nil, wantErr
		default:
			t.Fatalf("unexpected launchctl command: %v", arguments)
			return nil, nil
		}
	})

	err := controller.Start(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want state query error %v", err, wantErr)
	}
	if printCalls < 2 {
		t.Fatalf("launchctl print call count = %d, want bounded retries", printCalls)
	}
}

func TestDarwinStartDoesNotIgnoreKickstartFailure(t *testing.T) {
	wantErr := errors.New("operation not permitted")
	callCount := 0
	controller := newDarwinTestController(func(_ context.Context, arguments ...string) ([]byte, error) {
		callCount++
		return []byte("permission denied"), wantErr
	})

	err := controller.Start(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want %v", err, wantErr)
	}
	if callCount != 1 {
		t.Fatalf("launchctl call count = %d, want 1", callCount)
	}
}

func TestDarwinLaunchctlPreservesCommandTimeout(t *testing.T) {
	controller := newDarwinTestController(func(ctx context.Context, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, errors.New("signal: killed")
	})
	controller.config.timeout = time.Millisecond

	err := controller.launchctl(context.Background(), "kickstart", "-k", "system/"+launchLabel)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("launchctl() error = %v, want context deadline exceeded", err)
	}
	if !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("launchctl() error = %v, want process failure detail", err)
	}
}

// newDarwinTestController 创建使用可控 launchctl 行为的测试控制器。
func newDarwinTestController(runLaunchctl darwinCommandRunner) *darwinController {
	return &darwinController{
		config:            controllerConfig{timeout: 25 * time.Millisecond},
		runLaunchctl:      runLaunchctl,
		statePollInterval: time.Millisecond,
	}
}
