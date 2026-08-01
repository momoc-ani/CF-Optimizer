//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const windowsServiceName = "CFOptimizer"

type windowsController struct{ config controllerConfig }

func newPlatformController(cfg controllerConfig) Controller { return &windowsController{config: cfg} }

func (c *windowsController) Install(ctx context.Context) error {
	status, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if status.Installed {
		return errors.New("CFOptimizer service is already installed")
	}
	commandLine := fmt.Sprintf(`"%s" --config "%s"`, c.config.executable, c.config.configPath)
	if err := c.sc(ctx, "create", windowsServiceName, "binPath=", commandLine, "start=", "auto", "DisplayName=", "CF Optimizer"); err != nil {
		return err
	}
	if err := c.sc(ctx, "description", windowsServiceName, "Cloudflare node optimization and verified direct-route service"); err != nil {
		return err
	}
	if err := c.sc(ctx, "failure", windowsServiceName, "reset=", "86400", "actions=", "restart/10000/restart/30000/restart/60000"); err != nil {
		return err
	}
	return c.Start(ctx)
}

func (c *windowsController) Uninstall(ctx context.Context) error {
	_ = c.Stop(ctx)
	status, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if !status.Installed {
		return nil
	}
	return c.sc(ctx, "delete", windowsServiceName)
}

func (c *windowsController) Start(ctx context.Context) error {
	return c.sc(ctx, "start", windowsServiceName)
}

func (c *windowsController) Stop(ctx context.Context) error {
	return c.sc(ctx, "stop", windowsServiceName)
}

func (c *windowsController) Status(ctx context.Context) (Status, error) {
	commandContext, cancel := context.WithTimeout(ctx, c.config.timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "sc.exe", "query", windowsServiceName).CombinedOutput()
	text := string(output)
	if err != nil {
		if strings.Contains(text, "1060") {
			return Status{}, nil
		}
		return Status{}, fmt.Errorf("query Windows service: %w", err)
	}
	return Status{Installed: true, Enabled: true, Running: strings.Contains(text, "RUNNING"), Detail: strings.TrimSpace(text)}, nil
}

func (c *windowsController) sc(ctx context.Context, arguments ...string) error {
	commandContext, cancel := context.WithTimeout(ctx, c.config.timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "sc.exe", arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sc.exe %s failed: %w: %s", arguments[0], err, strings.TrimSpace(string(output)))
	}
	return nil
}
