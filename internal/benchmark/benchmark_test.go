package benchmark

import (
	"context"
	"net"
	"net/netip"
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
