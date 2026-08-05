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

// newTestAdapter 为临时 Hosts 文件关闭真实系统缓存刷新，避免测试修改开发机解析状态。
func newTestAdapter(t *testing.T, path string) *Adapter {
	t.Helper()
	adapter, err := New(config.HostsConfig{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	adapter.refreshResolver = func(context.Context) error { return nil }
	return adapter
}

func TestHostsLifecyclePreservesUnmanagedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	previous := "127.0.0.1 localhost\r\n"
	if err := os.WriteFile(path, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := newTestAdapter(t, path)
	refreshes := 0
	adapter.refreshResolver = func(context.Context) error {
		refreshes++
		return nil
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
	if refreshes != 3 {
		t.Fatalf("resolver cache refreshes = %d, want 3", refreshes)
	}
	restored, _ := os.ReadFile(path)
	if string(restored) != previous {
		t.Fatalf("Hosts was not restored: %q", restored)
	}
	if _, err := os.Stat(path + ".cf-optimizer.backup"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new backup file was not removed: %v", err)
	}
}

func TestHostsVerifyReportsResolverRefreshFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	previous := []byte("127.0.0.1 localhost\n")
	if err := os.WriteFile(path, previous, 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := newTestAdapter(t, path)
	policy := proxy.DirectPolicy{DomainMappings: []proxy.DomainMapping{{Domain: "ani.momoc.top", Addresses: []string{"172.64.154.64"}}}}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("resolver refresh failed")
	adapter.refreshResolver = func(context.Context) error { return wantErr }
	if err := adapter.Verify(context.Background(), policy, receipt); !errors.Is(err, wantErr) {
		t.Fatalf("verify error = %v, want %v", err, wantErr)
	}
	adapter.refreshResolver = func(context.Context) error { return nil }
	if err := adapter.Rollback(context.Background(), receipt); err != nil {
		t.Fatalf("rollback Hosts after refresh failure: %v", err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, previous) {
		t.Fatalf("Hosts was not restored after refresh failure: %q", restored)
	}
}

func TestHostsRollbackChainRestoresOriginalBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	backupPath := path + ".cf-optimizer.backup"
	previous := []byte("127.0.0.1 localhost\n172.66.2.98 cdn.example.com\n")
	backupPrevious := []byte("user-owned backup\n")
	if err := os.WriteFile(path, previous, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, backupPrevious, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := newTestAdapter(t, path)
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
		applied, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(applied), "172.66.2.98 cdn.example.com") {
			t.Fatalf("conflicting mapping reappeared during receipt chain: %q", applied)
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

func TestHostsLifecycleTemporarilySuppressesConflictingMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	previous := "127.0.0.1 localhost\r\n172.66.2.98 ani.momoc.top\r\n"
	if err := os.WriteFile(path, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := newTestAdapter(t, path)
	policy := proxy.DirectPolicy{DomainMappings: []proxy.DomainMapping{{Domain: "ani.momoc.top", Addresses: []string{"104.16.145.180"}}}}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Summary) != 1 || !strings.Contains(plan.Summary[0], "temporarily suppress 1 conflicting entries") {
		t.Fatalf("plan did not report the conflicting entry: %#v", plan.Summary)
	}
	receipt, err := adapter.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(applied), "172.66.2.98 ani.momoc.top") || !strings.Contains(string(applied), "104.16.145.180 ani.momoc.top") {
		t.Fatalf("conflicting Hosts mapping was not replaced: %q", applied)
	}
	if err := adapter.Rollback(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != previous {
		t.Fatalf("conflicting Hosts mapping was not restored: %q", restored)
	}
}

func TestHostsCleanupConflictPreservesCurrentFileAndRestoresManagedBackup(t *testing.T) {
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
	adapter := newTestAdapter(t, path)
	policy := proxy.DirectPolicy{DomainMappings: []proxy.DomainMapping{{Domain: "ani.momoc.top", Addresses: []string{"104.25.254.143"}}}}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	external := []byte("127.0.0.1 localhost\n104.17.28.80 ani.momoc.top\n")
	if err := os.WriteFile(path, external, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CleanupConflict(context.Background(), []proxy.Receipt{receipt}); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backupCurrent, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, external) || !bytes.Equal(backupCurrent, backupPrevious) {
		t.Fatalf("conflict cleanup changed unowned Hosts content: hosts=%q backup=%q", current, backupCurrent)
	}
}

func TestHostsCleanupConflictRejectsUnprovenBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := newTestAdapter(t, path)
	policy := proxy.DirectPolicy{DomainMappings: []proxy.DomainMapping{{Domain: "ani.momoc.top", Addresses: []string{"104.25.254.143"}}}}
	plan, err := adapter.Plan(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".cf-optimizer.backup", []byte("externally changed backup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CleanupConflict(context.Background(), []proxy.Receipt{receipt}); err == nil {
		t.Fatal("expected unproven backup conflict to be rejected")
	}
}

func TestSuppressConflictingMappingsPreservesOtherAliases(t *testing.T) {
	content := []byte("  172.66.2.98 ANI.MOMOC.TOP. keep.example # user mapping\ninvalid ani.momoc.top\n")
	updated, suppressed := suppressConflictingMappings(content, []string{"ani.momoc.top"}, "\n")
	if suppressed != 1 {
		t.Fatalf("suppressed entries = %d, want 1", suppressed)
	}
	want := "  172.66.2.98 keep.example # user mapping\ninvalid ani.momoc.top\n"
	if string(updated) != want {
		t.Fatalf("updated Hosts content = %q, want %q", updated, want)
	}
}

func TestHostsRejectsMalformedManagedBlock(t *testing.T) {
	if _, _, err := removeManagedBlock([]byte(beginMarker + "\nentry\n")); err == nil {
		t.Fatal("expected unterminated block error")
	}
}
