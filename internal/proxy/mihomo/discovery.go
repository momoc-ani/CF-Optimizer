package mihomo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

const autoProbeTimeout = 1200 * time.Millisecond

type controllerCandidate struct {
	Controller string
	Process    string
}

// AutoDetect 从本机 Mihomo/Clash 监听进程中发现控制端，并以只读版本请求确认身份。
func AutoDetect(ctx context.Context, cfg config.MihomoConfig) (proxy.Detection, error) {
	candidates := make([]controllerCandidate, 0, 4)
	if isLoopbackController(cfg.Controller) {
		candidates = append(candidates, controllerCandidate{Controller: cfg.Controller, Process: "配置端点"})
	}
	discovered, err := platformControllerCandidates(ctx)
	if err != nil {
		return proxy.Detection{Present: false}, fmt.Errorf("枚举本机 Mihomo 监听端口: %w", err)
	}
	candidates = append(candidates, discovered...)
	return probeControllerCandidates(ctx, cfg, candidates)
}

// probeControllerCandidates 按稳定顺序探测去重后的候选控制端。
func probeControllerCandidates(ctx context.Context, cfg config.MihomoConfig, candidates []controllerCandidate) (proxy.Detection, error) {
	candidates = uniqueControllerCandidates(candidates)
	var probeErrors []error
	for _, candidate := range candidates {
		probeConfig := cfg
		probeConfig.Controller = candidate.Controller
		if probeConfig.Timeout.Duration() <= 0 || probeConfig.Timeout.Duration() > autoProbeTimeout {
			probeConfig.Timeout = config.Duration(autoProbeTimeout)
		}
		adapter, err := New(probeConfig)
		if err != nil {
			probeErrors = append(probeErrors, fmt.Errorf("%s: %w", candidate.Controller, err))
			continue
		}
		detection, err := adapter.Detect(ctx)
		if err != nil || !detection.Present {
			if err != nil {
				probeErrors = append(probeErrors, fmt.Errorf("%s: %w", candidate.Controller, err))
			}
			continue
		}
		detection.Endpoint = candidate.Controller
		if candidate.Process == "" {
			detection.Message = "自动发现的控制 API 可访问"
		} else {
			detection.Message = fmt.Sprintf("已从进程 %s 自动发现控制 API", candidate.Process)
		}
		return detection, nil
	}
	if len(candidates) == 0 {
		return proxy.Detection{Present: false, Message: "未发现本机 Mihomo/Clash 监听进程"}, nil
	}
	return proxy.Detection{Present: false, Message: "发现疑似 Mihomo/Clash 进程，但控制 API 不可访问"}, errors.Join(probeErrors...)
}

// uniqueControllerCandidates 去重候选并保持显式配置端点优先、其余端点稳定排序。
func uniqueControllerCandidates(candidates []controllerCandidate) []controllerCandidate {
	seen := make(map[string]struct{}, len(candidates))
	result := make([]controllerCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.Controller = strings.TrimRight(strings.TrimSpace(candidate.Controller), "/")
		if candidate.Controller == "" {
			continue
		}
		if _, exists := seen[candidate.Controller]; exists {
			continue
		}
		seen[candidate.Controller] = struct{}{}
		result = append(result, candidate)
	}
	if len(result) > 1 {
		discovered := result[1:]
		sort.SliceStable(discovered, func(i, j int) bool {
			return discovered[i].Controller < discovered[j].Controller
		})
	}
	return result
}

// isLoopbackController 只允许自动探测访问本机回环控制端。
func isLoopbackController(rawController string) bool {
	controller, err := url.Parse(rawController)
	if err != nil || controller.Host == "" || (controller.Scheme != "http" && controller.Scheme != "https") {
		return false
	}
	host := strings.ToLower(controller.Hostname())
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// isMihomoProcess 判断监听进程名称是否属于 Mihomo 或 Clash 内核家族。
func isMihomoProcess(processName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(processName))
	return strings.Contains(normalized, "mihomo") || strings.Contains(normalized, "clash")
}
