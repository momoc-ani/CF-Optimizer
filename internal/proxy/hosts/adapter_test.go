package hosts

import (
	"bytes"
	"context"
	"errors"
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
	adapter, err := New(config.HostsConfig{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	policy := proxy.DirectPolicy{DomainMappings: []proxy.DomainMapping{{Domain: "cdn.example.com", Addresses: []string{"1.1.1.1"}}}}
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
	if err := adapter.Rollback(context.Background(), receipt); err != nil {
		t.Fatalf("rollback should be idempotent: %v", err)
	}
	restored, _ := os.ReadFile(path)
	if string(restored) != previous {
		t.Fatalf("Hosts was not restored: %q", restored)
	}
	if _, err := os.Stat(path + ".cf-optimizer.backup"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new backup file was not removed: %v", err)
	}
}

func TestHostsRollbackChainRestoresOriginalBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	backupPath := path + ".cf-optimizer.backup"
	previous := []byte("127.0.0.1 localhost\n")
	backupPrevious := []byte("user-owned backup\n")
	if err := os.WriteFile(path, previous, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, backupPrevious, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(config.HostsConfig{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	var receipts []proxy.Receipt
	for _, address := range []string{"1.1.1.1", "1.0.0.1"} {
		policy := proxy.DirectPolicy{DomainMappings: []proxy.DomainMapping{{Domain: "cdn.example.com", Addresses: []string{address}}}}
		plan, planErr := adapter.Plan(context.Background(), policy)
		if planErr != nil {
			t.Fatal(planErr)
		}
		receipt, applyErr := adapter.Apply(context.Background(), plan)
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		receipts = append(receipts, receipt)
	}
	for index := len(receipts) - 1; index >= 0; index-- {
		if err := adapter.Rollback(context.Background(), receipts[index]); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backupRestored, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, previous) || !bytes.Equal(backupRestored, backupPrevious) {
		t.Fatalf("rollback chain did not restore original files: hosts=%q backup=%q", restored, backupRestored)
	}
}

func TestHostsRejectsMalformedManagedBlock(t *testing.T) {
	if _, _, err := removeManagedBlock([]byte(beginMarker + "\nentry\n")); err == nil {
		t.Fatal("expected unterminated block error")
	}
}
