package daemon

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/application"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/ipc"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

func TestExponentialDelayIsBounded(t *testing.T) {
	maximum := 6 * time.Hour
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	for index, expected := range want {
		if actual := exponentialDelay(index+1, maximum); actual != expected {
			t.Fatalf("failure %d: got %s, want %s", index+1, actual, expected)
		}
	}
	if actual := exponentialDelay(100, maximum); actual != maximum {
		t.Fatalf("delay exceeded bound: %s", actual)
	}
}

func TestScheduledRunErrorCodeClassifiesCancellationAndConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "none", want: ""},
		{name: "context cancellation", err: context.Canceled, want: "cancelled"},
		{name: "IPC cancellation", err: &ipc.Error{Code: "cancelled", Message: "optimization was cancelled"}, want: "cancelled"},
		{name: "IPC conflict", err: &ipc.Error{Code: "conflict", Message: "already running"}, want: "conflict"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := scheduledRunErrorCode(test.err); got != test.want {
				t.Fatalf("scheduledRunErrorCode() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestScheduleRespondsToRuntimeConfigChanges(t *testing.T) {
	stateStore, err := store.Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Default()
	cfg.Schedule.Enabled = false
	cfg.Network.CommandTimeout = config.Duration(100 * time.Millisecond)
	runtimeState := &application.Runtime{Config: cfg, Store: stateStore, Logger: logger}
	api, err := application.NewAPI(runtimeState)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{runtime: runtimeState, api: api, logger: logger}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.schedule(ctx) }()

	waitForScheduleStatus(t, api, func(status application.ScheduleStatus) bool { return !status.Enabled && status.Trigger == "disabled" })
	next := cfg
	next.Schedule.Enabled = true
	next.Schedule.Interval = config.Duration(7 * time.Hour)
	runtimeState.ActivateSession(application.RuntimeSession{Config: next})
	waitForScheduleStatus(t, api, func(status application.ScheduleStatus) bool {
		return status.Enabled && status.Interval == "7h0m0s" && status.Trigger == "interval" && status.NextScheduledAt != nil
	})
	runtimeState.ActivateSession(application.RuntimeSession{Config: cfg})
	waitForScheduleStatus(t, api, func(status application.ScheduleStatus) bool { return !status.Enabled && status.Trigger == "disabled" })

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
}

// waitForScheduleStatus 等待调度器发布满足条件的可观察状态。
func waitForScheduleStatus(t *testing.T, api *application.API, matches func(application.ScheduleStatus) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := api.Handle(context.Background(), ipc.Request{Method: "system.status"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		status := response.(map[string]any)["schedule"].(application.ScheduleStatus)
		if matches(status) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scheduler status did not reflect the runtime configuration")
}
