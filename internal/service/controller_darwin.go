//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
)

const (
	launchLabel      = "com.cfoptimizer.daemon"
	launchDaemonPath = "/Library/LaunchDaemons/com.cfoptimizer.daemon.plist"
	managedPlistMark = "<!-- Managed by CF Optimizer -->"
)

type darwinController struct{ config controllerConfig }

func newPlatformController(cfg controllerConfig) Controller { return &darwinController{config: cfg} }

func (c *darwinController) Install(ctx context.Context) error {
	if err := refuseUnmanagedPlist(launchDaemonPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := fsutil.WriteFileAtomic(launchDaemonPath, []byte(c.plist()), 0o644); err != nil {
		return err
	}
	_ = c.launchctl(ctx, "bootout", "system/"+launchLabel)
	return c.launchctl(ctx, "bootstrap", "system", launchDaemonPath)
}

func (c *darwinController) Uninstall(ctx context.Context) error {
	_ = c.launchctl(ctx, "bootout", "system/"+launchLabel)
	if err := refuseUnmanagedPlist(launchDaemonPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.Remove(launchDaemonPath)
}

func (c *darwinController) Start(ctx context.Context) error {
	return c.launchctl(ctx, "kickstart", "-k", "system/"+launchLabel)
}

func (c *darwinController) Stop(ctx context.Context) error {
	return c.launchctl(ctx, "kill", "SIGTERM", "system/"+launchLabel)
}

func (c *darwinController) Status(ctx context.Context) (Status, error) {
	_, fileErr := os.Stat(launchDaemonPath)
	installed := fileErr == nil
	commandContext, cancel := context.WithTimeout(ctx, c.config.timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "launchctl", "print", "system/"+launchLabel).CombinedOutput()
	if err != nil {
		return Status{Installed: installed}, nil
	}
	text := string(output)
	return Status{Installed: installed, Enabled: true, Running: strings.Contains(text, "state = running"), Detail: strings.TrimSpace(text)}, nil
}

func (c *darwinController) plist() string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
%s
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>--config</string><string>%s</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>/var/log/cf-optimizer.stdout.log</string>
  <key>StandardErrorPath</key><string>/var/log/cf-optimizer.stderr.log</string>
</dict>
</plist>
`, managedPlistMark, launchLabel, xmlEscape(c.config.executable), xmlEscape(c.config.configPath))
}

func (c *darwinController) launchctl(ctx context.Context, arguments ...string) error {
	commandContext, cancel := context.WithTimeout(ctx, c.config.timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "launchctl", arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s failed: %w: %s", arguments[0], err, strings.TrimSpace(string(output)))
	}
	return nil
}

func refuseUnmanagedPlist(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.Contains(string(content), managedPlistMark) {
		return fmt.Errorf("refusing to overwrite unmanaged file %s", path)
	}
	return nil
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}
