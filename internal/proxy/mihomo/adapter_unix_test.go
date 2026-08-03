//go:build !windows

package mihomo

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
)

func TestAdapterDetectsMihomoThroughUnixController(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "cf-optimizer-mihomo-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socketPath := filepath.Join(directory, "controller.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "unix-test-secret"
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/version" || request.Header.Get("Authorization") != "Bearer "+secret {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = response.Write([]byte(`{"version":"v1.19.19"}`))
	})}
	serveError := make(chan error, 1)
	go func() { serveError <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-serveError; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("HTTP server error = %v", err)
		}
	})

	controller := (&url.URL{Scheme: unixControllerScheme, Path: socketPath}).String()
	cfg := config.Default().Proxy.Mihomo
	cfg.Controller = controller
	cfg.Secret = secret
	cfg.Timeout = config.Duration(time.Second)
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	detection, err := adapter.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !detection.Present || detection.Version != "v1.19.19" || detection.Endpoint != controller {
		t.Fatalf("unexpected detection: %#v", detection)
	}
}
