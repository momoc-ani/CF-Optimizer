package mihomo

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

type benchmarkTestAddr string

func (a benchmarkTestAddr) Network() string { return "tcp" }
func (a benchmarkTestAddr) String() string  { return string(a) }

type benchmarkTestConn struct{ net.Conn }

func (benchmarkTestConn) LocalAddr() net.Addr { return benchmarkTestAddr("192.0.2.20:54321") }

func TestVerifyBenchmarkPathRequiresDirectMihomoChain(t *testing.T) {
	tests := []struct {
		name          string
		connections   []map[string]any
		expectsError  bool
		proxyObserved bool
		direct        bool
	}{
		{
			name: "direct connection",
			connections: []map[string]any{{
				"metadata": map[string]string{"sourceIP": "192.0.2.20", "sourcePort": "54321", "destinationIP": "1.1.1.1", "destinationPort": "443"},
				"chains":   []string{"DIRECT"}, "rule": "IP-CIDR", "rulePayload": "1.1.1.1/32",
			}},
			proxyObserved: true, direct: true,
		},
		{
			name: "proxied connection",
			connections: []map[string]any{{
				"metadata": map[string]string{"sourceIP": "192.0.2.20", "sourcePort": "54321", "destinationIP": "1.1.1.1", "destinationPort": "443"},
				"chains":   []string{"Proxy"}, "rule": "MATCH", "rulePayload": "",
			}},
			expectsError: true,
		},
		{name: "connection not intercepted", connections: []map[string]any{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/connections" {
					response.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(response).Encode(map[string]any{"connections": test.connections})
			}))
			defer server.Close()
			cfg := config.Default().Proxy.Mihomo
			cfg.Controller = server.URL
			cfg.ProviderFile = filepath.Join(t.TempDir(), "provider.yaml")
			cfg.Timeout = config.Duration(40 * time.Millisecond)
			adapter, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var peer net.Conn
			adapter.SetBenchmarkDialer("en0", func(context.Context, string, string) (net.Conn, error) {
				clientConnection, serverConnection := net.Pipe()
				peer = serverConnection
				return benchmarkTestConn{Conn: clientConnection}, nil
			})
			t.Cleanup(func() {
				if peer != nil {
					_ = peer.Close()
				}
			})
			evidence, verifyErr := adapter.VerifyBenchmarkPath(context.Background(), []netip.Addr{netip.MustParseAddr("1.1.1.1")})
			if (verifyErr != nil) != test.expectsError {
				t.Fatalf("unexpected verification error: %v", verifyErr)
			}
			if test.expectsError && !strings.Contains(verifyErr.Error(), "not DIRECT") {
				t.Fatalf("unexpected non-DIRECT error: %v", verifyErr)
			}
			if !test.expectsError && (evidence.ProxyObserved != test.proxyObserved || evidence.DirectVerified != test.direct || !evidence.SocketBound || evidence.Interface != "en0") {
				t.Fatalf("unexpected benchmark path evidence: %#v", evidence)
			}
		})
	}
}

// TestCoordinatorBenchmarkGuardRestoresMihomoFiles 验证临时测速规则不会污染用户的活动配置与受管文件。
func TestCoordinatorBenchmarkGuardRestoresMihomoFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/version":
			_, _ = response.Write([]byte(`{"version":"1.19.19"}`))
		case "/configs":
			response.WriteHeader(http.StatusNoContent)
		case "/rules":
			_ = json.NewEncoder(response).Encode(map[string]any{"rules": []map[string]string{{"type": "IPCIDR", "payload": "1.1.1.1/32", "proxy": "DIRECT"}}})
		case "/connections":
			_ = json.NewEncoder(response).Encode(map[string]any{"connections": []map[string]any{{
				"metadata": map[string]string{"sourceIP": "192.0.2.20", "sourcePort": "54321", "destinationIP": "1.1.1.1", "destinationPort": "443"},
				"chains":   []string{"DIRECT"}, "rule": "IP-CIDR", "rulePayload": "1.1.1.1/32",
			}}})
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	providerPath := filepath.Join(directory, "cf-optimizer.yaml")
	activeConfigPath := filepath.Join(directory, "config.yaml")
	metadataPath := managedMetadataPath(providerPath)
	providerBefore := []byte("payload:\n  - IP-CIDR,9.9.9.9/32,DIRECT\n")
	activeConfigBefore := []byte("rules:\n  - MATCH,proxy\n")
	metadataBefore, err := json.Marshal(managedMetadata{
		Version: managedMetadataVersion, OriginalHosts: map[string]originalHostValue{}, OriginalRules: map[string]bool{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		providerPath: providerBefore, activeConfigPath: activeConfigBefore, metadataPath: metadataBefore,
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default().Proxy.Mihomo
	cfg.Enabled = true
	cfg.Controller = server.URL
	cfg.ProviderFile = providerPath
	cfg.ReloadConfig = activeConfigPath
	cfg.Timeout = config.Duration(100 * time.Millisecond)
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var peer net.Conn
	adapter.SetBenchmarkDialer("en0", func(context.Context, string, string) (net.Conn, error) {
		clientConnection, serverConnection := net.Pipe()
		peer = serverConnection
		return benchmarkTestConn{Conn: clientConnection}, nil
	})
	t.Cleanup(func() {
		if peer != nil {
			_ = peer.Close()
		}
	})
	coordinator, err := proxy.NewCoordinator([]proxy.Adapter{adapter}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	guard, err := coordinator.BeginBenchmarkGuard(
		context.Background(), proxy.DirectPolicy{IPv4CIDRs: []string{"1.1.1.1/32"}}, []netip.Addr{netip.MustParseAddr("1.1.1.1")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(guard.Receipts) != 1 || len(guard.Evidence) != 1 || !guard.Evidence[0].DirectVerified {
		t.Fatalf("unexpected benchmark guard result: %#v", guard)
	}
	if err := coordinator.EndBenchmarkGuard(context.Background(), guard); err != nil {
		t.Fatal(err)
	}

	for path, expected := range map[string][]byte{
		providerPath: providerBefore, activeConfigPath: activeConfigBefore, metadataPath: metadataBefore,
	} {
		actual, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(actual) != string(expected) {
			t.Fatalf("Mihomo file %s was not restored: got %q want %q", path, actual, expected)
		}
	}
}
