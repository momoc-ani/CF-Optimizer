package hosts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

func TestHostsLifecyclePreservesUnmanagedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	previous := "127.0.0.1 localhost\r\n"
	if err := os.WriteFile(path, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(config.HostsConfig{Enabled: true, Path: path, Domains: []string{"cdn.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	policy := proxy.DirectPolicy{IPv4CIDRs: []string{"1.1.1.1/32"}, Domains: []string{"cdn.example.com"}}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "127.0.0.1 localhost") || !strings.Contains(string(content), "1.1.1.1 cdn.example.com") || strings.Count(string(content), beginMarker) != 1 {
		t.Fatalf("unexpected Hosts content: %q", content)
	}
	if err := adapter.Verify(context.Background(), policy, receipt); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Rollback(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(path)
	if string(restored) != previous {
		t.Fatalf("Hosts was not restored: %q", restored)
	}
}

func TestHostsRejectsMalformedManagedBlock(t *testing.T) {
	if _, _, err := removeManagedBlock([]byte(beginMarker + "\nentry\n")); err == nil {
		t.Fatal("expected unterminated block error")
	}
}
