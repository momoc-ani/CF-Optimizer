package acceleration

import (
	"bytes"
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDownloadTesterDiscoversAndMeasuresSameOriginResource(t *testing.T) {
	asset := bytes.Repeat([]byte("domain-speed-probe"), 1<<15)
	var mutex sync.Mutex
	var observedHost string
	var observedRange string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		observedHost = request.Host
		if request.URL.Path == "/assets/app.js" && request.Header.Get("Range") != "bytes=0-0" {
			observedRange = request.Header.Get("Range")
		}
		mutex.Unlock()
		switch request.URL.Path {
		case "/":
			response.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = response.Write([]byte(`<html><script src="/assets/app.js"></script></html>`))
		case "/assets/app.js":
			http.ServeContent(response, request, "app.js", time.Unix(1, 0), bytes.NewReader(asset))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	domain := server.Certificate().DNSNames[0]
	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(server.Certificate())
	dialer := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	tester, err := NewDownloadTester(dialer, DownloadOptions{
		DiscoveryTimeout: 2 * time.Second,
		DownloadTimeout:  2 * time.Second,
		MaxBytes:         128 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	tester.rootCAs = rootCAs

	probeURL, err := tester.DiscoverProbeURL(context.Background(), domain, "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(probeURL, "/assets/app.js") {
		t.Fatalf("DiscoverProbeURL() = %q", probeURL)
	}
	result, err := tester.Measure(context.Background(), domain, "1.1.1.1", probeURL)
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloaded != 128<<10 || result.Mbps <= 0 {
		t.Fatalf("unexpected domain download result: %#v", result)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if observedHost != domain || observedRange != "bytes=0-131071" {
		t.Fatalf("domain request did not preserve Host or range: host=%q range=%q", observedHost, observedRange)
	}
}

func TestDownloadTesterRejectsPageWithoutLargeSameOriginResource(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<html><script src="https://cdn.example.net/app.js"></script></html>`))
	}))
	defer server.Close()
	domain := server.Certificate().DNSNames[0]
	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(server.Certificate())
	tester, err := NewDownloadTester(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}, DownloadOptions{DiscoveryTimeout: time.Second, DownloadTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	tester.rootCAs = rootCAs

	_, err = tester.DiscoverProbeURL(context.Background(), domain, "1.1.1.1")
	if err == nil || !strings.Contains(err.Error(), "no same-origin") {
		t.Fatalf("DiscoverProbeURL() error = %v", err)
	}
}
