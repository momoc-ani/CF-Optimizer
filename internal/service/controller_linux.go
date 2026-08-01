//go:build linux

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
)

const (
	linuxServiceName = "cf-optimizer.service"
	linuxUnitPath    = "/etc/systemd/system/cf-optimizer.service"
	managedUnitMark  = "# Managed by CF Optimizer"
)

type linuxController struct{ config controllerConfig }

func newPlatformController(cfg controllerConfig) Controller { return &linuxController{config: cfg} }

func (c *linuxController) Install(ctx context.Context) error {
	if err := refuseUnmanagedFile(linuxUnitPath, managedUnitMark); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := fsutil.WriteFileAtomic(linuxUnitPath, []byte(c.unit()), 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	if err := c.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return c.systemctl(ctx, "enable", "--now", linuxServiceName)
}

func (c *linuxController) Uninstall(ctx context.Context) error {
	_ = c.systemctl(ctx, "disable", "--now", linuxServiceName)
	if err := refuseUnmanagedFile(linuxUnitPath, managedUnitMark); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(linuxUnitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return c.systemctl(ctx, "daemon-reload")
}

func (c *linuxController) Start(ctx context.Context) error {
	return c.systemctl(ctx, "start", linuxServiceName)
}

func (c *linuxController) Stop(ctx context.Context) error {
	return c.systemctl(ctx, "stop", linuxServiceName)
}

func (c *linuxController) Status(ctx context.Context) (Status, error) {
	status := Status{}
	if _, err := os.Stat(linuxUnitPath); err == nil {
		status.Installed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return status, err
	}
	status.Running = c.systemctl(ctx, "is-active", "--quiet", linuxServiceName) == nil
	status.Enabled = c.systemctl(ctx, "is-enabled", "--quiet", linuxServiceName) == nil
	return status, nil
}

func (c *linuxController) unit() string {
	writePaths := []string{c.config.config.DataDir, filepath.Dir(c.config.configPath)}
	for _, path := range []string{c.config.config.Proxy.Mihomo.ProviderFile, c.config.config.Proxy.SingBox.ManagedFile, c.config.config.Proxy.Xray.ManagedFile} {
		if path != "" {
			writePaths = append(writePaths, filepath.Dir(path))
		}
	}
	var pathLines strings.Builder
	for _, path := range uniqueStrings(writePaths) {
		pathLines.WriteString("ReadWritePaths=")
		pathLines.WriteString(systemdQuote(path))
		pathLines.WriteByte('\n')
	}
	return fmt.Sprintf(`%s
[Unit]
Description=CF Optimizer background service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s --config %s
Restart=on-failure
RestartSec=10s
NoNewPrivileges=true
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW
ProtectSystem=full
ProtectHome=read-only
PrivateTmp=true
%s
[Install]
WantedBy=multi-user.target
`, managedUnitMark, systemdQuote(c.config.executable), systemdQuote(c.config.configPath), pathLines.String())
}

func (c *linuxController) systemctl(ctx context.Context, arguments ...string) error {
	commandContext, cancel := context.WithTimeout(ctx, c.config.timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "systemctl", arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s failed: %w: %s", arguments[0], err, strings.TrimSpace(string(output)))
	}
	return nil
}

func systemdQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func refuseUnmanagedFile(path, marker string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(content), marker) {
		return fmt.Errorf("refusing to overwrite unmanaged file %s", path)
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
