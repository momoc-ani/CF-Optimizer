//go:build windows

package mihomo

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
)

func TestAdapterDetectsMihomoThroughWindowsNamedPipe(t *testing.T) {
	pipePath := fmt.Sprintf(`\\.\pipe\cf-optimizer-mihomo-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := winio.ListenPipe(pipePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/version" {
			http.NotFound(response, request)
			return
		}
		_, _ = response.Write([]byte(`{"version":"1.19.25"}`))
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-serveDone
	})

	controller, err := namedPipeControllerEndpoint(pipePath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().Proxy.Mihomo
	cfg.Controller = controller
	cfg.Timeout = config.Duration(2 * time.Second)
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	detection, err := adapter.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !detection.Present || detection.Version != "1.19.25" || detection.Endpoint != controller {
		t.Fatalf("unexpected detection: %#v", detection)
	}
}
