package benchmark

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
)

type fakeConn struct{ net.Conn }

func (fakeConn) Close() error { return nil }

type repeatingReader struct{}

func (repeatingReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'x'
	}
	return len(buffer), nil
}

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
	cfg.DownloadURL = ""
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

func TestDownloadSplitsCloudflareRequestAndAccumulatesThroughput(t *testing.T) {
	cfg := config.Default().Benchmark
	cfg.DownloadDuration = config.Duration(5 * time.Second)
	cfg.DownloadMaxBytes = 6_000_000
	tester := New(cfg, nil)
	client, server := net.Pipe()
	defer client.Close()

	requestSizes := make(chan []int64, 1)
	serverErrors := make(chan error, 1)
	go func() {
		defer server.Close()
		reader := bufio.NewReader(server)
		var sizes []int64
		var sent int64
		for sent < cfg.DownloadMaxBytes {
			request, err := http.ReadRequest(reader)
			if err != nil {
				serverErrors <- err
				return
			}
			size, err := strconv.ParseInt(request.URL.Query().Get("bytes"), 10, 64)
			if err != nil {
				serverErrors <- err
				return
			}
			sizes = append(sizes, size)
			if _, err := fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: keep-alive\r\n\r\n", size); err != nil {
				serverErrors <- err
				return
			}
			if _, err := io.CopyN(server, repeatingReader{}, size); err != nil {
				serverErrors <- err
				return
			}
			sent += size
		}
		requestSizes <- sizes
		serverErrors <- nil
	}()

	target, err := url.Parse(config.DefaultDownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	result := Result{Qualified: true, TLSVerified: true}
	tester.download(context.Background(), client, target, &result)
	if err := <-serverErrors; err != nil {
		t.Fatal(err)
	}
	sizes := <-requestSizes
	if len(sizes) != 2 || sizes[0] != cloudflareDownloadChunkBytes || sizes[1] != 1_000_000 {
		t.Fatalf("official download request sizes = %#v, want [5000000 1000000]", sizes)
	}
	if !result.Qualified || !result.DownloadVerified || result.Downloaded != cfg.DownloadMaxBytes || result.Mbps <= 0 {
		t.Fatalf("chunked download result is incomplete: %#v", result)
	}
}
