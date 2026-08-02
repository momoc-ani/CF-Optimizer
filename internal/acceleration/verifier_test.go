package acceleration

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
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
