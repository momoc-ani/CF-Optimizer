//go:build linux

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
)

func TestLinuxUnitContainsCapabilityAndWritablePaths(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = "/var/lib/cf-optimizer"
	cfg.Proxy.Mihomo.ProviderFile = "/etc/mihomo/providers/cf-optimizer.yaml"
	controller := &linuxController{config: controllerConfig{executable: "/usr/bin/cf-optimizerd", configPath: "/etc/cf-optimizer/config.yaml", config: cfg}}
	unit := controller.unit()
	for _, expected := range []string{"CAP_NET_ADMIN", "CAP_NET_RAW", `ExecStart="/usr/bin/cf-optimizerd"`, `ReadWritePaths="/etc/mihomo/providers"`} {
		if !strings.Contains(unit, expected) {
			t.Fatalf("unit does not contain %q:\n%s", expected, unit)
		}
	}
}

func TestRefuseUnmanagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service")
	if err := os.WriteFile(path, []byte("unmanaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := refuseUnmanagedFile(path, managedUnitMark); err == nil {
		t.Fatal("expected unmanaged file refusal")
	}
}
