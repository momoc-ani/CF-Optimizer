package benchmark

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
)

type fakeConn struct{ net.Conn }

func (fakeConn) Close() error { return nil }

func TestScorePenalizesLossAndLatency(t *testing.T) {
	fast := Result{Qualified: true, Attempts: 4, Successes: 4, AvgLatency: 20 * time.Millisecond, Jitter: 2 * time.Millisecond}
	slow := Result{Qualified: true, Attempts: 4, Successes: 3, Loss: .25, AvgLatency: 200 * time.Millisecond, Jitter: 50 * time.Millisecond}
	if Score(fast, false) <= Score(slow, false) {
		t.Fatalf("fast score %.2f should exceed slow score %.2f", Score(fast, false), Score(slow, false))
	}
}

func TestTCPQuality(t *testing.T) {
	cfg := config.Default().Benchmark
	cfg.ConnectAttempts = 2
	cfg.Concurrency = 1
	cfg.TLSServerName = ""
	dial := func(context.Context, string, string) (net.Conn, error) { return fakeConn{}, nil }
	tester := New(cfg, dial)
	results, err := tester.Run(context.Background(), []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Qualified || results[0].Successes != 2 {
		t.Fatalf("unexpected result: %#v", results)
	}
}

func TestRunConcurrentDownloadProbesLimitsWorkers(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	var completed atomic.Int32
	err := runConcurrentDownloadProbes(context.Background(), 12, 5, func(context.Context, int) {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
		completed.Add(1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := maximum.Load(); got > 5 || got < 2 {
		t.Fatalf("download worker count = %d, want between 2 and 5", got)
	}
	if completed.Load() != 12 {
		t.Fatalf("completed probes = %d, want 12", completed.Load())
	}
}

func TestRunConcurrentDownloadProbesHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	err := runConcurrentDownloadProbes(ctx, 10, 5, func(context.Context, int) {
		select {
		case started <- struct{}{}:
			cancel()
		default:
		}
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context.Canceled", err)
	}
}
