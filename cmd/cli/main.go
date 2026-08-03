package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/application"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/daemon"
	"github.com/cf-optimizer/cf-optimizer/internal/ipc"
	"github.com/cf-optimizer/cf-optimizer/internal/logging"
	"github.com/cf-optimizer/cf-optimizer/internal/optimizer"
	"github.com/cf-optimizer/cf-optimizer/internal/service"
	"github.com/cf-optimizer/cf-optimizer/internal/version"
)

type options struct {
	configPath    string
	dataDirectory string
	jsonOutput    bool
	logLevel      string
}

const (
	serviceOperationTimeout     = 30 * time.Second
	managedPolicyCleanupTimeout = 2 * time.Minute
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "cf-optimizer:", err)
		os.Exit(1)
	}
}

func run(arguments []string, stdout, stderr io.Writer) error {
	global := flag.NewFlagSet("cf-optimizer", flag.ContinueOnError)
	global.SetOutput(stderr)
	settings := options{}
	global.StringVar(&settings.configPath, "config", config.DefaultConfigPath(), "YAML configuration path")
	global.StringVar(&settings.dataDirectory, "data-dir", "", "override state directory")
	global.BoolVar(&settings.jsonOutput, "json", false, "emit JSON progress events")
	global.StringVar(&settings.logLevel, "log-level", "info", "debug, info, warn, or error")
	global.Usage = func() { printUsage(stderr) }
	if err := global.Parse(arguments); err != nil {
		return err
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		printUsage(stderr)
		return errors.New("command is required")
	}
	command := remaining[0]
	commandArguments := remaining[1:]
	if command == "version" {
		return writeJSON(stdout, version.Metadata())
	}
	if command == "init" {
		return initializeConfig(settings, commandArguments, stdout)
	}
	cfg, err := config.Load(settings.configPath, settings.dataDirectory)
	if err != nil {
		return err
	}
	switch command {
	case "install", "uninstall", "start", "stop", "service-status":
		return serviceCommand(context.Background(), command, commandArguments, settings, cfg, stdout)
	case "run":
		return runForeground(settings, cfg)
	case "benchmark":
		return runDirectBenchmark(settings, cfg, stdout, stderr)
	case "cleanup":
		return cleanupCommand(context.Background(), settings, cfg, stdout)
	case "status", "optimize", "cancel", "test-route", "history", "logs", "ranges", "proxy":
		return ipcCommand(command, commandArguments, settings, cfg, stdout, stderr)
	case "config":
		return configCommand(commandArguments, cfg, stdout)
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", command)
	}
}

// cleanupCommand 拒绝与已注册后台服务并发清理，再进入不依赖物理出口的恢复流程。
func cleanupCommand(ctx context.Context, settings options, cfg config.Config, output io.Writer) error {
	current, err := os.Executable()
	if err != nil {
		return err
	}
	controller, err := service.NewController(current, settings.configPath, cfg)
	if err != nil {
		return err
	}
	statusContext, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()
	status, err := controller.Status(statusContext)
	if err != nil {
		return err
	}
	if status.Running {
		return errors.New("stop the CF Optimizer service before cleaning managed policy")
	}
	return cleanupManagedPolicy(ctx, settings, cfg, output)
}

func initializeConfig(settings options, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	force := flags.Bool("force", false, "replace an existing managed config")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if !*force {
		if _, err := os.Stat(settings.configPath); err == nil {
			return fmt.Errorf("config already exists at %s; use --force to replace it", settings.configPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	cfg := config.Default()
	if settings.dataDirectory != "" {
		cfg.DataDir = settings.dataDirectory
	}
	if cfg.DataDir == "" {
		cfg.DataDir = config.DefaultDataDir()
	}
	if cfg.IPC.Endpoint == "" {
		cfg.IPC.Endpoint = config.DefaultEndpoint(cfg.DataDir)
	}
	if err := config.Save(settings.configPath, cfg); err != nil {
		return err
	}
	return writeJSON(output, map[string]string{"config": settings.configPath, "data_dir": cfg.DataDir})
}

func serviceCommand(ctx context.Context, command string, arguments []string, settings options, cfg config.Config, output io.Writer) error {
	daemonPath := ""
	if command == "install" {
		flags := flag.NewFlagSet("install", flag.ContinueOnError)
		flags.StringVar(&daemonPath, "daemon", "", "absolute cf-optimizerd path")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if daemonPath == "" {
			current, err := os.Executable()
			if err != nil {
				return err
			}
			name := "cf-optimizerd"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			daemonPath = filepath.Join(filepath.Dir(current), name)
		}
	} else {
		current, err := os.Executable()
		if err != nil {
			return err
		}
		daemonPath = current
	}
	absoluteConfig, err := filepath.Abs(settings.configPath)
	if err != nil {
		return err
	}
	absoluteDaemon, err := filepath.Abs(daemonPath)
	if err != nil {
		return err
	}
	controller, err := service.NewController(absoluteDaemon, absoluteConfig, cfg)
	if err != nil {
		return err
	}
	operationContext, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()
	switch command {
	case "install":
		if _, err := os.Stat(absoluteDaemon); err != nil {
			return fmt.Errorf("daemon executable: %w", err)
		}
		if _, err := os.Stat(absoluteConfig); errors.Is(err, os.ErrNotExist) {
			if err := config.Save(absoluteConfig, cfg); err != nil {
				return err
			}
		}
		currentStatus, err := controller.Status(operationContext)
		if err != nil {
			return err
		}
		if !currentStatus.Installed {
			if err := controller.Install(operationContext); err != nil {
				return err
			}
		} else if !currentStatus.Running {
			if err := controller.Start(operationContext); err != nil {
				return err
			}
		}
	case "uninstall":
		currentStatus, err := controller.Status(operationContext)
		if err != nil {
			return err
		}
		if currentStatus.Running {
			if err := controller.Stop(operationContext); err != nil {
				return err
			}
		}
		if err := cleanupManagedPolicy(ctx, settings, cfg, io.Discard); err != nil {
			return fmt.Errorf("clean managed policy before uninstall: %w", err)
		}
		removeContext, removeCancel := context.WithTimeout(ctx, serviceOperationTimeout)
		defer removeCancel()
		if err := controller.Uninstall(removeContext); err != nil {
			return err
		}
	case "start":
		if err := controller.Start(operationContext); err != nil {
			return err
		}
	case "stop":
		if err := controller.Stop(operationContext); err != nil {
			return err
		}
	}
	statusContext, statusCancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer statusCancel()
	status, statusErr := controller.Status(statusContext)
	if statusErr != nil {
		return statusErr
	}
	return writeJSON(output, status)
}

// cleanupManagedPolicy 在服务停止后恢复受管路由和代理配置，不删除用户配置或运行历史。
func cleanupManagedPolicy(parent context.Context, settings options, cfg config.Config, output io.Writer) error {
	logger, closer, err := logging.New(cfg.DataDir, settings.logLevel, false)
	if err != nil {
		return err
	}
	defer closer.Close()
	runtimeState, err := application.BuildCleanup(cfg, settings.configPath, logger)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, managedPolicyCleanupTimeout)
	defer cancel()
	if err := runtimeState.CleanupManagedPolicy(ctx); err != nil {
		return err
	}
	logger.Info("受管策略清理完成", "component", "cleanup", "result", "completed")
	return writeJSON(output, map[string]bool{"cleaned": true})
}

func runForeground(settings options, cfg config.Config) error {
	logger, closer, err := logging.New(cfg.DataDir, settings.logLevel, true)
	if err != nil {
		return err
	}
	defer closer.Close()
	runtimeState, err := application.Build(cfg, settings.configPath, logger)
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
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return backgroundService.Run(ctx)
}

func runDirectBenchmark(settings options, cfg config.Config, output, progressOutput io.Writer) error {
	if cfg.DataDir == config.DefaultDataDir() && settings.dataDirectory == "" {
		cfg.DataDir = config.UserDataDir()
		cfg.IPC.Endpoint = config.DefaultEndpoint(cfg.DataDir)
	}
	logger, closer, err := logging.New(cfg.DataDir, settings.logLevel, false)
	if err != nil {
		return err
	}
	defer closer.Close()
	runtimeState, err := application.Build(cfg, settings.configPath, logger)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	report, err := runtimeState.Runner.Run(ctx, optimizer.RunOptions{}, progressPrinter(progressOutput, settings.jsonOutput))
	if err != nil {
		return err
	}
	return writeJSON(output, report)
}

func ipcCommand(command string, arguments []string, settings options, cfg config.Config, output, progressOutput io.Writer) error {
	client, err := ipc.NewClient(cfg.IPC.Endpoint)
	if err != nil {
		return err
	}
	method := ""
	parameters := any(map[string]any{})
	switch command {
	case "status":
		method = "system.status"
	case "optimize":
		method = "optimizer.run"
		parameters = optimizer.RunOptions{ApplyPolicy: true}
	case "cancel":
		method = "optimizer.cancel"
	case "test-route":
		if len(arguments) != 1 {
			return errors.New("test-route requires one target IP")
		}
		method = "diagnostics.route"
		parameters = map[string]string{"target": arguments[0]}
	case "history":
		method = "history.list"
	case "logs":
		method = "logs.tail"
		lines := 200
		flags := flag.NewFlagSet("logs", flag.ContinueOnError)
		flags.IntVar(&lines, "lines", 200, "number of log lines")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		parameters = map[string]int{"lines": lines}
	case "ranges":
		if len(arguments) != 1 || (arguments[0] != "get" && arguments[0] != "update") {
			return errors.New("ranges requires get or update")
		}
		method = "ranges." + arguments[0]
	case "proxy":
		if len(arguments) != 1 || arguments[0] != "detect" {
			return errors.New("proxy requires detect")
		}
		method = "proxy.detect"
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	result, err := client.Call(ctx, method, parameters, func(event json.RawMessage) error {
		if settings.jsonOutput {
			_, err := fmt.Fprintln(progressOutput, string(event))
			return err
		}
		var progress optimizer.Event
		if err := json.Unmarshal(event, &progress); err != nil {
			return err
		}
		if progress.Progress != nil {
			_, err := fmt.Fprintf(progressOutput, "%s %d/%d qualified=%d\r", progress.Stage, progress.Progress.Completed, progress.Progress.Total, progress.Progress.Qualified)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	var decoded any
	if err := json.Unmarshal(result, &decoded); err != nil {
		return err
	}
	return writeJSON(output, decoded)
}

func configCommand(arguments []string, cfg config.Config, output io.Writer) error {
	if len(arguments) != 1 || (arguments[0] != "show" && arguments[0] != "validate") {
		return errors.New("config requires show or validate")
	}
	if arguments[0] == "validate" {
		if err := cfg.Validate(); err != nil {
			return err
		}
		return writeJSON(output, map[string]bool{"valid": true})
	}
	return writeJSON(output, cfg)
}

func progressPrinter(output io.Writer, jsonOutput bool) func(optimizer.Event) {
	return func(event optimizer.Event) {
		if jsonOutput {
			encoded, _ := json.Marshal(event)
			_, _ = fmt.Fprintln(output, string(encoded))
			return
		}
		if event.Progress != nil {
			_, _ = fmt.Fprintf(output, "%s %d/%d qualified=%d\r", event.Stage, event.Progress.Completed, event.Progress.Total, event.Progress.Qualified)
		}
	}
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, `Usage: cf-optimizer [global options] <command>

Commands:
  init [--force]             Write a default configuration
  install [--daemon PATH]    Install and start the system service
  uninstall                  Stop and remove the system service
  start | stop               Control the system service
  service-status             Query the system service manager
  run                         Run the daemon in the foreground
  status                      Read daemon state through local IPC
  benchmark                   Run a direct benchmark without applying policy
  cleanup                     Roll back managed routes and proxy policy
  optimize                    Benchmark and apply verified policy through daemon
  cancel                      Cancel the active optimization
  test-route IP               Collect direct-route evidence for an IP
  ranges get|update           Inspect or refresh Cloudflare ranges
  proxy detect                Detect configured proxy adapters
  history                     List optimization history
  logs [--lines N]            Show recent structured logs
  config show|validate        Inspect validated configuration
  version                     Show build metadata`)
}
