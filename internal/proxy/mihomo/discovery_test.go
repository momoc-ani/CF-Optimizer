package mihomo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
)

func TestProbeControllerCandidatesFindsMihomoWithoutFixedPort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/version" {
			http.NotFound(response, request)
			return
		}
		_, _ = response.Write([]byte(`{"version":"1.19.2"}`))
	}))
	defer server.Close()
	cfg := config.Default().Proxy.Mihomo
	cfg.Timeout = config.Duration(time.Second)
	detection, err := probeControllerCandidates(context.Background(), cfg, []controllerCandidate{
		{Controller: "http://127.0.0.1:1", Process: "verge-mihomo"},
		{Controller: server.URL, Process: "verge-mihomo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !detection.Present || detection.Version != "1.19.2" || detection.Endpoint != server.URL {
		t.Fatalf("unexpected detection: %#v", detection)
	}
}

func TestAdapterDetectRejectsUnrelatedVersionEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"name":"unrelated"}`))
	}))
	defer server.Close()
	cfg := config.Default().Proxy.Mihomo
	cfg.Controller = server.URL
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	detection, err := adapter.Detect(context.Background())
	if err == nil || detection.Present {
		t.Fatalf("unrelated endpoint was accepted: detection=%#v err=%v", detection, err)
	}
}
