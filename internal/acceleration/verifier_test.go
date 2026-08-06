package acceleration

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

type deadlineRecordingConn struct {
	deadline time.Time
}

func (c *deadlineRecordingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *deadlineRecordingConn) Write(buffer []byte) (int, error) { return len(buffer), nil }
func (c *deadlineRecordingConn) Close() error                     { return nil }
func (c *deadlineRecordingConn) LocalAddr() net.Addr              { return testAddr("127.0.0.1:12345") }
func (c *deadlineRecordingConn) RemoteAddr() net.Addr             { return testAddr("127.0.0.1:443") }
func (c *deadlineRecordingConn) SetDeadline(deadline time.Time) error {
	c.deadline = deadline
	return nil
}
func (c *deadlineRecordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *deadlineRecordingConn) SetWriteDeadline(time.Time) error { return nil }

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }

func TestVerifierAppliesTimeoutToRawConnection(t *testing.T) {
	connection := &deadlineRecordingConn{}
	verifier, err := NewVerifier(func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}, 25*time.Millisecond)
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	startedAt := time.Now()
	if _, err := verifier.request(context.Background(), "example.com", "127.0.0.1:443"); err == nil {
		t.Fatal("incomplete TLS connection must fail verification")
	}
	if connection.deadline.IsZero() {
		t.Fatal("raw connection must receive the verification deadline")
	}
	if !connection.deadline.After(startedAt) || connection.deadline.After(startedAt.Add(time.Second)) {
		t.Fatalf("unexpected connection deadline %s", connection.deadline)
	}
}

func TestVerifyAppliedRetriesUntilSystemMappingPropagates(t *testing.T) {
	verifier, err := NewVerifier(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unexpected raw dial")
	}, time.Second)
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	verifier.appliedRetryInterval = time.Nanosecond
	requests := 0
	verifier.requestConnection = func(_ context.Context, domain, address string) (string, error) {
		requests++
		if domain != "ani.momoc.top" || address != "ani.momoc.top:443" {
			t.Fatalf("unexpected verification target %s %s", domain, address)
		}
		if requests == 1 {
			return "104.21.92.119", nil
		}
		return "172.64.154.64", nil
	}
	mappings := []proxy.DomainMapping{{Domain: "ani.momoc.top", Addresses: []string{"172.64.154.64"}}}
	if err := verifier.VerifyApplied(context.Background(), mappings); err != nil {
		t.Fatalf("verify propagated mapping: %v", err)
	}
	if requests != 2 {
		t.Fatalf("verification requests = %d, want 2", requests)
	}
}

func TestVerifyPreflightRetriesTransientCandidateFailure(t *testing.T) {
	verifier, err := NewVerifier(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unexpected raw dial")
	}, time.Second)
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	requests := 0
	verifier.requestConnection = func(context.Context, string, string) (string, error) {
		requests++
		if requests == 1 {
			return "", context.DeadlineExceeded
		}
		return "104.16.244.3", nil
	}
	if err := verifier.VerifyPreflight(context.Background(), []proxy.DomainMapping{{Domain: "ani.momoc.top", Addresses: []string{"104.16.244.3"}}}); err != nil {
		t.Fatalf("verify preflight after transient failure: %v", err)
	}
	if requests != 2 {
		t.Fatalf("preflight requests = %d, want 2", requests)
	}
}

func TestVerifyAppliedRejectsStaleSystemMappingAfterRetryLimit(t *testing.T) {
	verifier, err := NewVerifier(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unexpected raw dial")
	}, time.Second)
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	verifier.appliedRetryInterval = time.Nanosecond
	requests := 0
	verifier.requestConnection = func(context.Context, string, string) (string, error) {
		requests++
		return "104.21.92.119", nil
	}
	mappings := []proxy.DomainMapping{{Domain: "ani.momoc.top", Addresses: []string{"172.64.154.64"}}}
	err = verifier.VerifyApplied(context.Background(), mappings)
	if err == nil || !strings.Contains(err.Error(), "connected to 104.21.92.119 instead of an optimized address") {
		t.Fatalf("unexpected stale mapping result: %v", err)
	}
	if requests != appliedVerificationMaxAttempts {
		t.Fatalf("verification requests = %d, want %d", requests, appliedVerificationMaxAttempts)
	}
}

func TestVerifyAppliedClassifiesCandidateConnectionFailure(t *testing.T) {
	verifier, err := NewVerifierWithOptions(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unexpected raw dial")
	}, VerificationOptions{
		PreflightTimeout: time.Second,
		ApplyTimeout:     time.Second,
		AttemptTimeout:   50 * time.Millisecond,
		RetryInterval:    time.Nanosecond,
		MaxAttempts:      1,
	})
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	verifier.requestConnection = func(context.Context, string, string) (string, error) {
		return "", context.DeadlineExceeded
	}
	err = verifier.VerifyApplied(context.Background(), []proxy.DomainMapping{{Domain: "ani.momoc.top", Addresses: []string{"104.25.241.29"}}})
	var verificationErr *proxy.DomainVerificationError
	if !errors.As(err, &verificationErr) {
		t.Fatalf("verification error = %v, want typed domain error", err)
	}
	if verificationErr.Kind != proxy.DomainVerificationCandidateUnreachable || verificationErr.Address != "104.25.241.29" {
		t.Fatalf("unexpected candidate failure details: %#v", verificationErr)
	}
}

func TestVerifyAppliedClassifiesMappingPropagationFailure(t *testing.T) {
	verifier, err := NewVerifierWithOptions(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unexpected raw dial")
	}, VerificationOptions{
		PreflightTimeout: time.Second,
		ApplyTimeout:     time.Second,
		AttemptTimeout:   50 * time.Millisecond,
		RetryInterval:    time.Nanosecond,
		MaxAttempts:      1,
	})
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	verifier.requestConnection = func(context.Context, string, string) (string, error) {
		return "172.66.2.98", nil
	}
	err = verifier.VerifyApplied(context.Background(), []proxy.DomainMapping{{Domain: "ani.momoc.top", Addresses: []string{"104.25.241.29"}}})
	var verificationErr *proxy.DomainVerificationError
	if !errors.As(err, &verificationErr) {
		t.Fatalf("verification error = %v, want typed domain error", err)
	}
	if verificationErr.Kind != proxy.DomainVerificationMappingNotPropagated || !strings.Contains(verificationErr.Error(), "connected to 172.66.2.98") {
		t.Fatalf("unexpected propagation failure details: %#v", verificationErr)
	}
}

func TestVerifyAppliedRetriesCandidateWithinIndependentAttemptTimeout(t *testing.T) {
	verifier, err := NewVerifierWithOptions(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unexpected raw dial")
	}, VerificationOptions{
		PreflightTimeout: 10 * time.Millisecond,
		ApplyTimeout:     time.Second,
		AttemptTimeout:   100 * time.Millisecond,
		RetryInterval:    time.Nanosecond,
		MaxAttempts:      2,
	})
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	requests := 0
	verifier.requestConnection = func(ctx context.Context, _, _ string) (string, error) {
		requests++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= verifier.timeout {
			t.Fatalf("apply attempt reused preflight timeout: deadline=%s preflight=%s", deadline, verifier.timeout)
		}
		if requests == 1 {
			return "", errors.New("temporary connection failure")
		}
		return "104.25.241.29", nil
	}
	if err := verifier.VerifyApplied(context.Background(), []proxy.DomainMapping{{Domain: "ani.momoc.top", Addresses: []string{"104.25.241.29"}}}); err != nil {
		t.Fatalf("verify retry: %v", err)
	}
	if requests != 2 {
		t.Fatalf("verification requests = %d, want 2", requests)
	}
}

func TestValidateHTTPSResponseRejectsCloudflareEdgeIPRestricted(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Body:       io.NopCloser(strings.NewReader("<h1>Error 1034</h1><p>Edge IP Restricted</p>")),
	}
	if err := validateHTTPSResponse(response); err == nil {
		t.Fatal("Cloudflare Error 1034 must fail domain preflight")
	}
}

func TestValidateHTTPSResponseAllowsOrdinaryApplicationError(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Body:       io.NopCloser(strings.NewReader(`{"error":"authentication required"}`)),
	}
	if err := validateHTTPSResponse(response); err != nil {
		t.Fatalf("application status should still prove edge compatibility: %v", err)
	}
}
