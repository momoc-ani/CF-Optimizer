//go:build darwin

package hosts

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	darwinResolverRefreshTimeout = 5 * time.Second
	darwinDSCacheUtilPath        = "/usr/bin/dscacheutil"
	darwinKillallPath            = "/usr/bin/killall"
)

type resolverCacheCommand func(context.Context, string, ...string) error

// refreshResolverCache 刷新 macOS 目录缓存并通知 mDNSResponder 重新加载 Hosts 映射。
func refreshResolverCache(ctx context.Context) error {
	refreshContext, cancel := context.WithTimeout(ctx, darwinResolverRefreshTimeout)
	defer cancel()
	return refreshDarwinResolverCache(refreshContext, runResolverCacheCommand)
}

// refreshDarwinResolverCache 按固定顺序执行 macOS 解析缓存刷新，便于无系统副作用地测试命令契约。
func refreshDarwinResolverCache(ctx context.Context, run resolverCacheCommand) error {
	commands := []struct {
		path string
		args []string
	}{
		{path: darwinDSCacheUtilPath, args: []string{"-flushcache"}},
		{path: darwinKillallPath, args: []string{"-HUP", "mDNSResponder"}},
	}
	for _, command := range commands {
		if err := run(ctx, command.path, command.args...); err != nil {
			return err
		}
	}
	return nil
}

// runResolverCacheCommand 不经过 shell 执行固定系统工具，并保留可诊断的非敏感输出。
func runResolverCacheCommand(ctx context.Context, path string, args ...string) error {
	output, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("run %s: %w", path, err)
	}
	return fmt.Errorf("run %s: %w: %s", path, err, detail)
}
