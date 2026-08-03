//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
)

const (
	launchLabel      = "com.cfoptimizer.daemon"
	launchDaemonPath = "/Library/LaunchDaemons/com.cfoptimizer.daemon.plist"
	managedPlistMark = "<!-- Managed by CF Optimizer -->"
	launchdStatePoll = 200 * time.Millisecond
)

type darwinCommandRunner func(context.Context, ...string) ([]byte, error)

type darwinController struct {
	config            controllerConfig
	runLaunchctl      darwinCommandRunner
	statePollInterval time.Duration
}

func newPlatformController(cfg controllerConfig) Controller {
	return &darwinController{config: cfg, runLaunchctl: executeLaunchctl}
}

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

// Start 启动 launchd 服务，并在 kickstart 超时后核验服务是否已实际运行。
func (c *darwinController) Start(ctx context.Context) error {
	err := c.launchctl(ctx, "kickstart", "-k", "system/"+launchLabel)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	running, _, statusErr := c.waitForRunning(ctx)
	if statusErr != nil {
		return fmt.Errorf("verify service state after launchctl kickstart timeout: %w", errors.Join(err, statusErr))
	}
	if !running {
		return fmt.Errorf("service is not running after launchctl kickstart timeout: %w", err)
	}
	return nil
}

// waitForRunning 在单次命令超时后给 launchd 一个有上限的异步启动确认窗口。
func (c *darwinController) waitForRunning(ctx context.Context) (bool, string, error) {
	verificationContext, cancel := context.WithTimeout(ctx, c.config.timeout)
	defer cancel()
	interval := c.statePollInterval
	if interval <= 0 {
		interval = launchdStatePoll
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastDetail string
	var lastErr error
	for {
		running, detail, err := c.launchdState(verificationContext)
		if err == nil {
			lastDetail = detail
			lastErr = nil
			if running {
				return true, detail, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-verificationContext.Done():
			return false, lastDetail, errors.Join(verificationContext.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func (c *darwinController) Stop(ctx context.Context) error {
	return c.launchctl(ctx, "kill", "SIGTERM", "system/"+launchLabel)
}

// Status 返回 launchd 服务的安装和运行状态。
func (c *darwinController) Status(ctx context.Context) (Status, error) {
	_, fileErr := os.Stat(launchDaemonPath)
	installed := fileErr == nil
	running, detail, err := c.launchdState(ctx)
	if err != nil {
		return Status{Installed: installed}, nil
	}
	return Status{Installed: installed, Enabled: true, Running: running, Detail: detail}, nil
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

// launchctl 执行无需读取输出的 launchctl 命令。
func (c *darwinController) launchctl(ctx context.Context, arguments ...string) error {
	_, err := c.launchctlOutput(ctx, arguments...)
	return err
}

// launchdState 查询 launchd 服务状态，并保留原始详情供诊断输出使用。
func (c *darwinController) launchdState(ctx context.Context) (bool, string, error) {
	output, err := c.launchctlOutput(ctx, "print", "system/"+launchLabel)
	if err != nil {
		return false, "", err
	}
	detail := strings.TrimSpace(string(output))
	return strings.Contains(detail, "state = running"), detail, nil
}

// launchctlOutput 为单次 launchctl 调用施加超时，并保留上下文错误链和进程错误详情。
func (c *darwinController) launchctlOutput(ctx context.Context, arguments ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, c.config.timeout)
	defer cancel()
	runner := c.runLaunchctl
	if runner == nil {
		runner = executeLaunchctl
	}
	output, err := runner(commandContext, arguments...)
	if err != nil {
		cause := err
		if commandContext.Err() != nil {
			cause = commandContext.Err()
		}
		return nil, fmt.Errorf("launchctl %s failed: %w: %v: %s", arguments[0], cause, err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

// executeLaunchctl 通过系统 launchctl 执行服务生命周期命令。
func executeLaunchctl(ctx context.Context, arguments ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "launchctl", arguments...).CombinedOutput()
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
