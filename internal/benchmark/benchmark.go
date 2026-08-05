package benchmark

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
)

// Stage 标识 TCP、TLS 或下载测速阶段。
type Stage string

// 测速阶段用于进度事件和前端状态展示。
const (
	StageTCP      Stage = "tcp"
	StageTLS      Stage = "tls"
	StageDownload Stage = "download"
)

// Progress 汇总高频测速过程，避免为每次连接写入 Info 日志。
type Progress struct {
	Stage     Stage  `json:"stage"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
	IP        string `json:"ip,omitempty"`
	Qualified int    `json:"qualified"`
	Message   string `json:"message,omitempty"`
}

// Result 保存单个候选从 TCP 到 TLS/下载阶段的完整指标。
type Result struct {
	IP               netip.Addr    `json:"ip"`
	Family           int           `json:"family"`
	Attempts         int           `json:"attempts"`
	Successes        int           `json:"successes"`
	Loss             float64       `json:"loss"`
	AvgLatency       time.Duration `json:"avg_latency"`
	P95Latency       time.Duration `json:"p95_latency"`
	Jitter           time.Duration `json:"jitter"`
	TLSLatency       time.Duration `json:"tls_latency,omitempty"`
	TTFB             time.Duration `json:"ttfb,omitempty"`
	Downloaded       int64         `json:"downloaded,omitempty"`
	DownloadTime     time.Duration `json:"download_time,omitempty"`
	Mbps             float64       `json:"mbps,omitempty"`
	TCPQualified     bool          `json:"tcp_qualified"`
	TLSVerified      bool          `json:"tls_verified"`
	DownloadVerified bool          `json:"download_verified"`
	Qualified        bool          `json:"qualified"`
	Score            float64       `json:"score"`
	Error            string        `json:"error,omitempty"`
}

// Tester 使用显式直连 Dialer 执行有并发上限的两阶段测速。
type Tester struct {
	config config.BenchmarkConfig
	dial   cfnetwork.DialContextFunc
	now    func() time.Time
}

// New 创建测速器；调用方负责提供不使用系统代理的 Dialer。
func New(cfg config.BenchmarkConfig, dial cfnetwork.DialContextFunc) *Tester {
	return &Tester{config: cfg, dial: dial, now: time.Now}
}

// Run 执行 TCP 初筛和 TLS/可选 HTTPS 复筛，并按最终分数降序返回。
func (t *Tester) Run(ctx context.Context, addresses []netip.Addr, progress func(Progress)) ([]Result, error) {
	if len(addresses) == 0 {
		return nil, errors.New("no candidates to benchmark")
	}
	results := make([]Result, len(addresses))
	jobs := make(chan int)
	var workers sync.WaitGroup
	workerCount := min(t.config.Concurrency, len(addresses))
	completed := 0
	qualified := 0
	var progressMu sync.Mutex
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				result := t.testTCP(ctx, addresses[index])
				results[index] = result
				progressMu.Lock()
				completed++
				if result.Qualified {
					qualified++
				}
				if progress != nil {
					progress(Progress{Stage: StageTCP, Completed: completed, Total: len(addresses), IP: result.IP.String(), Qualified: qualified})
				}
				progressMu.Unlock()
			}
		}()
	}
	for index := range addresses {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return nil, ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Qualified != results[j].Qualified {
			return results[i].Qualified
		}
		return results[i].AvgLatency < results[j].AvgLatency
	})
	top := min(t.config.DownloadTop, qualified)
	downloadCompleted := 0
	var downloadProgressMu sync.Mutex
	if err := runConcurrentDownloadProbes(ctx, top, t.config.DownloadConcurrency, func(probeContext context.Context, index int) {
		t.probeTLSAndDownload(probeContext, &results[index])
		results[index].Score = Score(results[index], t.config.DownloadURL != "")
		if progress == nil || probeContext.Err() != nil {
			return
		}
		downloadProgressMu.Lock()
		defer downloadProgressMu.Unlock()
		downloadCompleted++
		stage := StageTLS
		if t.config.DownloadURL != "" {
			stage = StageDownload
		}
		progress(Progress{Stage: stage, Completed: downloadCompleted, Total: top, IP: results[index].IP.String(), Qualified: qualified})
	}); err != nil {
		return nil, err
	}
	for i := top; i < len(results); i++ {
		results[i].Qualified = false
		results[i].Score = 0
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	return results, nil
}

// runConcurrentDownloadProbes 使用受限 worker pool 执行 TLS/下载复筛，避免同时产生过多下载流量。
func runConcurrentDownloadProbes(ctx context.Context, total, concurrency int, probe func(context.Context, int)) error {
	if total <= 0 {
		return nil
	}
	if concurrency < 1 {
		return errors.New("download concurrency must be positive")
	}
	workerCount := min(total, concurrency)
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					probe(ctx, index)
				}
			}
		}()
	}
	for index := 0; index < total; index++ {
		select {
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return ctx.Err()
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()
	return ctx.Err()
}

func (t *Tester) testTCP(ctx context.Context, ip netip.Addr) Result {
	result := Result{IP: ip, Attempts: t.config.ConnectAttempts, Family: 6}
	if ip.Is4() {
		result.Family = 4
	}
	latencies := make([]time.Duration, 0, result.Attempts)
	var lastErr error
	for i := 0; i < result.Attempts; i++ {
		started := t.now()
		conn, err := t.dial(ctx, "tcp", net.JoinHostPort(ip.String(), "443"))
		latency := t.now().Sub(started)
		if err != nil {
			lastErr = err
			continue
		}
		_ = conn.Close()
		latencies = append(latencies, latency)
	}
	result.Successes = len(latencies)
	result.Loss = 1 - float64(result.Successes)/float64(result.Attempts)
	if len(latencies) == 0 {
		if lastErr != nil {
			result.Error = lastErr.Error()
		}
		return result
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var total time.Duration
	for _, latency := range latencies {
		total += latency
	}
	result.AvgLatency = total / time.Duration(len(latencies))
	p95Index := int(math.Ceil(float64(len(latencies))*0.95)) - 1
	result.P95Latency = latencies[max(0, p95Index)]
	if len(latencies) > 1 {
		var variance float64
		for _, latency := range latencies {
			delta := float64(latency - result.AvgLatency)
			variance += delta * delta
		}
		result.Jitter = time.Duration(math.Sqrt(variance / float64(len(latencies))))
	}
	result.TCPQualified = result.Loss <= t.config.LossLimit && result.AvgLatency <= t.config.LatencyLimit.Duration()
	result.Qualified = result.TCPQualified
	return result
}

func (t *Tester) probeTLSAndDownload(ctx context.Context, result *Result) {
	serverName := t.config.TLSServerName
	var targetURL *url.URL
	if t.config.DownloadURL != "" {
		parsed, err := url.Parse(t.config.DownloadURL)
		if err != nil {
			result.Error = err.Error()
			return
		}
		targetURL = parsed
		if serverName == "" {
			serverName = parsed.Hostname()
		}
	}
	if serverName == "" {
		return
	}
	dialCtx, cancel := context.WithTimeout(ctx, t.config.TLSTimeout.Duration())
	defer cancel()
	raw, err := t.dial(dialCtx, "tcp", net.JoinHostPort(result.IP.String(), "443"))
	if err != nil {
		result.Qualified = false
		result.Error = "TLS dial: " + err.Error()
		return
	}
	defer raw.Close()
	stopCancellation := context.AfterFunc(ctx, func() { _ = raw.SetDeadline(time.Now()) })
	defer stopCancellation()
	_ = raw.SetDeadline(time.Now().Add(t.config.TLSTimeout.Duration()))
	tlsConn := tls.Client(raw, &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12})
	started := t.now()
	if err := tlsConn.HandshakeContext(dialCtx); err != nil {
		result.Qualified = false
		result.Error = "TLS handshake: " + err.Error()
		return
	}
	result.TLSLatency = t.now().Sub(started)
	result.TLSVerified = true
	if targetURL == nil {
		return
	}
	t.download(ctx, tlsConn, targetURL, result)
}

func (t *Tester) download(ctx context.Context, conn *tls.Conn, target *url.URL, result *Result) {
	request := &http.Request{
		Method: http.MethodGet, URL: target, Host: target.Host,
		Header: http.Header{"User-Agent": []string{"CF-Optimizer/1"}, "Accept-Encoding": []string{"identity"}, "Connection": []string{"close"}},
	}
	if err := request.Write(conn); err != nil {
		result.Qualified = false
		result.Error = "write download request: " + err.Error()
		return
	}
	started := t.now()
	deadline := started.Add(t.config.DownloadDuration.Duration())
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = conn.SetDeadline(deadline)
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		result.Qualified = false
		result.Error = "read download response: " + err.Error()
		return
	}
	defer response.Body.Close()
	result.TTFB = t.now().Sub(started)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Qualified = false
		result.Error = fmt.Sprintf("download returned %s", response.Status)
		return
	}
	reader := io.LimitReader(response.Body, t.config.DownloadMaxBytes)
	written, readErr := io.Copy(io.Discard, reader)
	result.DownloadTime = t.now().Sub(started)
	result.Downloaded = written
	if written > 0 && result.DownloadTime > 0 {
		result.Mbps = float64(written*8) / result.DownloadTime.Seconds() / 1_000_000
	}
	if readErr != nil && !isExpectedDeadline(readErr) {
		result.Qualified = false
		result.Error = "download: " + readErr.Error()
		return
	}
	if written == 0 {
		result.Qualified = false
		result.Error = "download returned an empty body"
		return
	}
	result.DownloadVerified = true
}

func isExpectedDeadline(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// Score 将成功率、延迟、抖动、TLS 与吞吐量归一化为 0 到 100 分。
func Score(result Result, downloadEnabled bool) float64 {
	if !result.Qualified || result.Successes == 0 {
		return 0
	}
	availability := 40 * (1 - result.Loss)
	latency := 25 * math.Exp(-float64(result.AvgLatency)/float64(250*time.Millisecond))
	jitter := 10 * math.Exp(-float64(result.Jitter)/float64(100*time.Millisecond))
	tlsScore := 10.0
	if result.TLSLatency > 0 {
		tlsScore *= math.Exp(-float64(result.TLSLatency) / float64(500*time.Millisecond))
	} else if result.Error != "" {
		tlsScore = 0
	}
	speed := 15.0
	if downloadEnabled {
		speed = 15 * (1 - math.Exp(-result.Mbps/50))
	}
	return math.Round((availability+latency+jitter+tlsScore+speed)*100) / 100
}
