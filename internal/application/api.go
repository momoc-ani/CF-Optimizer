package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/diagnostics"
	"github.com/cf-optimizer/cf-optimizer/internal/ipc"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/optimizer"
	"github.com/cf-optimizer/cf-optimizer/internal/version"
)

// API 将运行时能力暴露为经过参数校验的本地 IPC 方法。
type API struct {
	runtime               *Runtime
	activeMutex           sync.Mutex
	activeCancel          context.CancelFunc
	activeEvent           *optimizer.Event
	configurationMutex    sync.Mutex
	quickStartMutex       sync.Mutex
	quickStartPlan        *quickStartPlanRecord
	discoverPhysicalPath  physicalPathDiscoverer
	networkFingerprint    networkFingerprinter
	detectManagedAdapters managedAdapterDetector
	buildManagedSession   managedSessionBuilder
	saveConfig            func(string, config.Config) error
	now                   func() time.Time
}

// NewAPI 创建后台服务业务处理器。
func NewAPI(runtime *Runtime) (*API, error) {
	if runtime == nil {
		return nil, errors.New("application runtime is required")
	}
	return &API{
		runtime: runtime, discoverPhysicalPath: cfnetwork.DiscoverPhysicalPath,
		networkFingerprint:    cfnetwork.NetworkFingerprint,
		detectManagedAdapters: runtime.DetectManagedAdapters,
		buildManagedSession:   runtime.BuildManagedSession,
		saveConfig:            config.Save,
		now:                   time.Now,
	}, nil
}

// Handle 路由白名单方法，并在每个边界严格解码参数。
func (a *API) Handle(ctx context.Context, request ipc.Request, emit func(any) error) (any, error) {
	switch request.Method {
	case "system.status":
		return a.systemStatus(), nil
	case "optimizer.run":
		return a.runOptimizer(ctx, request.Params, emit)
	case "optimizer.cancel":
		return a.cancelOptimizer()
	case "quickstart.plan":
		return a.planQuickStart(ctx, request.Params)
	case "quickstart.run":
		return a.runQuickStart(ctx, request.Params, emit)
	case "ranges.get":
		return a.runtime.Ranges.Load()
	case "ranges.update":
		return a.runtime.Ranges.Update(ctx, true)
	case "history.list":
		return a.runtime.Store.Snapshot().History, nil
	case "routes.list":
		return a.runtime.Routes.Transactions(), nil
	case "proxy.detect":
		return a.runtime.DetectProxyAdapters(ctx), nil
	case "diagnostics.route":
		return a.routeDiagnostics(ctx, request.Params)
	case "config.get":
		return a.runtime.View().Config, nil
	case "config.update":
		return a.updateConfig(request.Params)
	case "logs.tail":
		return a.tailLogs(request.Params)
	default:
		return nil, &ipc.Error{Code: "method_not_found", Message: "unknown method " + request.Method}
	}
}

func (a *API) systemStatus() map[string]any {
	a.activeMutex.Lock()
	activeEvent := cloneEvent(a.activeEvent)
	a.activeMutex.Unlock()
	view := a.runtime.View()
	return map[string]any{
		"build": version.Metadata(), "protocol_version": ipc.ProtocolVersion,
		"state": a.runtime.Store.Snapshot(), "physical_path": view.PhysicalPath,
		"active_event": activeEvent,
	}
}

func (a *API) runOptimizer(ctx context.Context, raw json.RawMessage, emit func(any) error) (optimizer.RunReport, error) {
	var parameters optimizer.RunOptions
	if err := decodeStrict(raw, &parameters); err != nil {
		return optimizer.RunReport{}, invalidParams(err)
	}
	return a.RunOptimization(ctx, parameters, func(event optimizer.Event) error { return emit(event) })
}

// RunOptimization 统一管理 IPC 与调度器发起任务的取消句柄。
func (a *API) RunOptimization(ctx context.Context, parameters optimizer.RunOptions, emit func(optimizer.Event) error) (optimizer.RunReport, error) {
	return a.runWithRunner(ctx, a.runtime.View().Runner, parameters, emit)
}

// runWithRunner 让普通任务和快速流程共享同一个可取消单任务边界。
func (a *API) runWithRunner(ctx context.Context, runner *optimizer.Runner, parameters optimizer.RunOptions, emit func(optimizer.Event) error) (optimizer.RunReport, error) {
	if runner == nil {
		return optimizer.RunReport{}, errors.New("optimizer runner is unavailable")
	}
	runContext, cancel := context.WithCancel(ctx)
	if !a.setActiveCancel(cancel) {
		cancel()
		return optimizer.RunReport{}, &ipc.Error{Code: "conflict", Message: optimizer.ErrAlreadyRunning.Error()}
	}
	defer func() {
		cancel()
		a.clearActiveCancel()
	}()
	var emitMutex sync.Mutex
	streamConnected := true
	report, err := runner.Run(runContext, parameters, func(event optimizer.Event) {
		a.setActiveEvent(event)
		emitMutex.Lock()
		defer emitMutex.Unlock()
		if !streamConnected || emit == nil {
			return
		}
		if eventErr := emit(event); eventErr != nil {
			streamConnected = false
			a.runtime.Logger.Warn("任务事件订阅已断开，后台任务继续运行", "component", "ipc", "run_id", event.RunID, "error", eventErr)
		}
	})
	if errors.Is(err, optimizer.ErrAlreadyRunning) {
		return report, &ipc.Error{Code: "conflict", Message: err.Error()}
	}
	if errors.Is(err, context.Canceled) {
		return report, &ipc.Error{Code: "cancelled", Message: "optimization was cancelled"}
	}
	return report, err
}

func (a *API) cancelOptimizer() (map[string]bool, error) {
	a.activeMutex.Lock()
	defer a.activeMutex.Unlock()
	if a.activeCancel == nil {
		return map[string]bool{"cancelled": false}, nil
	}
	a.activeCancel()
	return map[string]bool{"cancelled": true}, nil
}

func (a *API) setActiveCancel(cancel context.CancelFunc) bool {
	a.activeMutex.Lock()
	defer a.activeMutex.Unlock()
	if a.activeCancel != nil {
		return false
	}
	a.activeCancel = cancel
	return true
}

func (a *API) clearActiveCancel() {
	a.activeMutex.Lock()
	a.activeCancel = nil
	a.activeEvent = nil
	a.activeMutex.Unlock()
}

func (a *API) setActiveEvent(event optimizer.Event) {
	a.activeMutex.Lock()
	a.activeEvent = cloneEvent(&event)
	a.activeMutex.Unlock()
}

func cloneEvent(event *optimizer.Event) *optimizer.Event {
	if event == nil {
		return nil
	}
	clone := *event
	if event.Progress != nil {
		progress := *event.Progress
		clone.Progress = &progress
	}
	return &clone
}

func (a *API) routeDiagnostics(ctx context.Context, raw json.RawMessage) (diagnostics.Report, error) {
	var parameters struct {
		Target string `json:"target"`
	}
	if err := decodeStrict(raw, &parameters); err != nil {
		return diagnostics.Report{}, invalidParams(err)
	}
	target, err := netip.ParseAddr(parameters.Target)
	if err != nil || !target.IsGlobalUnicast() {
		return diagnostics.Report{}, invalidParams(errors.New("target must be a global unicast IP address"))
	}
	view := a.runtime.View()
	return diagnostics.Generate(ctx, target, view.PhysicalPath, a.runtime.RouteBackend, view.DirectDial, view.Config.Network.CommandTimeout.Duration()), nil
}

func (a *API) updateConfig(raw json.RawMessage) (map[string]bool, error) {
	var parameters struct {
		Config config.Config `json:"config"`
	}
	if err := decodeStrict(raw, &parameters); err != nil {
		return nil, invalidParams(err)
	}
	if !a.configurationMutex.TryLock() {
		return nil, &ipc.Error{Code: "conflict", Message: "configuration is locked by an active quick-start run"}
	}
	defer a.configurationMutex.Unlock()
	view := a.runtime.View()
	parameters.Config.Proxy.Mihomo.Secret = view.Config.Proxy.Mihomo.Secret
	if parameters.Config.DataDir == "" {
		parameters.Config.DataDir = view.Config.DataDir
	}
	if err := parameters.Config.Validate(); err != nil {
		return nil, invalidParams(err)
	}
	if a.runtime.ConfigPath == "" {
		return nil, &ipc.Error{Code: "precondition_failed", Message: "daemon has no configured config file path"}
	}
	if err := config.Save(a.runtime.ConfigPath, parameters.Config); err != nil {
		return nil, err
	}
	return map[string]bool{"saved": true, "restart_required": true}, nil
}

func (a *API) tailLogs(raw json.RawMessage) ([]string, error) {
	parameters := struct {
		Lines int `json:"lines"`
	}{Lines: 200}
	if err := decodeStrict(raw, &parameters); err != nil {
		return nil, invalidParams(err)
	}
	if parameters.Lines < 1 || parameters.Lines > 2000 {
		return nil, invalidParams(errors.New("lines must be between 1 and 2000"))
	}
	content, err := os.ReadFile(filepath.Join(a.runtime.View().Config.DataDir, "logs", "cf-optimizer.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) > parameters.Lines {
		lines = lines[len(lines)-parameters.Lines:]
	}
	return lines, nil
}

func decodeStrict(raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func invalidParams(err error) *ipc.Error {
	return &ipc.Error{Code: "invalid_params", Message: fmt.Sprintf("invalid parameters: %v", err)}
}
