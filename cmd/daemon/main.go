package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cf-optimizer/cf-optimizer/internal/application"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/daemon"
	"github.com/cf-optimizer/cf-optimizer/internal/logging"
	"github.com/cf-optimizer/cf-optimizer/internal/servicehost"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "cf-optimizerd:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("cf-optimizerd", flag.ContinueOnError)
	configPath := flags.String("config", config.DefaultConfigPath(), "YAML configuration path")
	dataDirectory := flags.String("data-dir", "", "override state directory")
	logLevel := flags.String("log-level", "info", "debug, info, warn, or error")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath, *dataDirectory)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Windows Service 必须先完成 SCM 注册，再执行可能受网络状态影响的运行时初始化。
	// 非 Windows 平台仍由 systemd/launchd 直接调用同一个初始化函数。
	return servicehost.Run(ctx, func(serviceContext context.Context) error {
		logger, closer, err := logging.New(cfg.DataDir, *logLevel, false)
		if err != nil {
			return err
		}
		defer closer.Close()
		runtimeState, err := application.Build(cfg, *configPath, logger)
		if err != nil {
			return err
		}
		api, err := application.NewAPI(runtimeState)
		if err != nil {
			return err
		}
		backgroundService, err := daemon.New(runtimeState, api)
		if err != nil {
			return err
		}
		return backgroundService.Run(serviceContext)
	})
}
