package mihomo

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type testVerificationTimeout struct{}

func (testVerificationTimeout) Error() string   { return "verification timed out" }
func (testVerificationTimeout) Timeout() bool   { return true }
func (testVerificationTimeout) Temporary() bool { return true }

func TestVerifyWithTransientRetryRetriesTimeoutOnce(t *testing.T) {
	attempts := 0
	err := verifyWithTransientRetry(context.Background(), time.Second, func(context.Context) error {
		attempts++
		if attempts == 1 {
			return testVerificationTimeout{}
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("transient timeout was not retried once: attempts=%d error=%v", attempts, err)
	}
}

func TestVerifyWithTransientRetryDoesNotRetryDeterministicError(t *testing.T) {
	attempts := 0
	expected := errors.New("connection is not DIRECT")
	err := verifyWithTransientRetry(context.Background(), time.Second, func(context.Context) error {
		attempts++
		return expected
	})
	if !errors.Is(err, expected) || attempts != 1 {
		t.Fatalf("deterministic error was retried: attempts=%d error=%v", attempts, err)
	}
}

func TestVerifyWithTransientRetryDoesNotRetryCanceledParent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	err := verifyWithTransientRetry(ctx, time.Second, func(context.Context) error {
		attempts++
		return context.DeadlineExceeded
	})
	if !errors.Is(err, context.DeadlineExceeded) || attempts != 1 {
		t.Fatalf("canceled parent was retried: attempts=%d error=%v", attempts, err)
	}
}

func TestVerifyWithTransientRetryWindowRetriesUnpropagatedMapping(t *testing.T) {
	attempts := 0
	err := verifyWithTransientRetryWindow(context.Background(), time.Second, time.Second, time.Nanosecond, 2, func(context.Context) error {
		attempts++
		if attempts == 1 {
			return &mappingNotPropagatedError{err: errors.New("mapping is still propagating")}
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("mapping propagation failure was not retried: attempts=%d error=%v", attempts, err)
	}
}

func TestMappingNotPropagatedErrorIsClassifiedAsTransient(t *testing.T) {
	err := &mappingNotPropagatedError{err: errors.New("Mihomo did not expose an active DIRECT connection")}
	if !isTransientVerificationError(err) {
		t.Fatal("mapping propagation error must be retriable")
	}
}

func TestReadMappedHTTPSResponseCompletesBeforeConnectionEvidence(t *testing.T) {
	request, err := http.NewRequest(http.MethodHead, "https://ani.example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	responseRead := false
	reader := bufio.NewReader(&responseTrackingReader{
		Reader: strings.NewReader("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"),
		read:   &responseRead,
	})
	err = verifyMappedHTTPSResponse(reader, request, "ani.example", func() error {
		if !responseRead {
			t.Fatal("connection evidence was queried before reading the HTTPS response")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadMappedHTTPSResponseRejectsServerFailure(t *testing.T) {
	request, err := http.NewRequest(http.MethodHead, "https://ani.example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	evidenceCalled := false
	err = verifyMappedHTTPSResponse(bufio.NewReader(strings.NewReader("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")), request, "ani.example", func() error {
		evidenceCalled = true
		return nil
	})
	if err == nil || evidenceCalled || !strings.Contains(err.Error(), "502") {
		t.Fatalf("502 response was not rejected before connection evidence: err=%v evidence=%t", err, evidenceCalled)
	}
}

type responseTrackingReader struct {
	*strings.Reader
	read *bool
}

func (r *responseTrackingReader) Read(buffer []byte) (int, error) {
	*r.read = true
	return r.Reader.Read(buffer)
}
