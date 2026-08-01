package external

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

const (
	adapterName     = "external"
	protocolVersion = "1.1"
	maxRPCOutput    = 1 << 20
)

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Version string `json:"version"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (w *limitedBuffer) Write(content []byte) (int, error) {
	if w.buffer.Len()+len(content) > w.limit {
		remaining := w.limit - w.buffer.Len()
		if remaining > 0 {
			_, _ = w.buffer.Write(content[:remaining])
		}
		return len(content), errors.New("external adapter output exceeds limit")
	}
	return w.buffer.Write(content)
}

// Adapter 通过每次调用独立进程的版本化 JSON-RPC 协议扩展代理能力。
type Adapter struct {
	config config.ExternalProxyConfig
}

// New 创建外部 JSON-RPC 适配器。
func New(cfg config.ExternalProxyConfig) (*Adapter, error) {
	if cfg.Executable == "" {
		return nil, errors.New("external adapter executable is required")
	}
	return &Adapter{config: cfg}, nil
}

// Name 返回稳定的适配器标识。
func (a *Adapter) Name() string { return adapterName }

// Capabilities 声明协议可表达全部统一策略；实际能力由 detect 结果进一步展示。
func (a *Adapter) Capabilities() proxy.Capabilities {
	return proxy.Capabilities{Processes: true, IPv4: true, IPv6: true, Domains: true, DomainMappings: true, HotReload: true, Rollback: true}
}

// Detect 调用外部进程的 detect 方法并解码状态。
func (a *Adapter) Detect(ctx context.Context) (proxy.Detection, error) {
	result, err := a.call(ctx, "detect", nil)
	if err != nil {
		return proxy.Detection{}, err
	}
	var detection proxy.Detection
	if err := json.Unmarshal(result, &detection); err != nil {
		return detection, fmt.Errorf("decode external detection: %w", err)
	}
	return detection, nil
}

// Plan 委托外部进程生成不产生副作用的版本化计划。
func (a *Adapter) Plan(ctx context.Context, policy proxy.DirectPolicy) (proxy.Plan, error) {
	result, err := a.call(ctx, "plan", map[string]any{"policy": policy})
	if err != nil {
		return proxy.Plan{}, err
	}
	return proxy.Plan{
		ID: fmt.Sprintf("external-%d", time.Now().UnixNano()), Adapter: adapterName, Policy: policy,
		Summary: []string{"apply external JSON-RPC DIRECT policy"}, Payload: result,
	}, nil
}

// Apply 传递先前生成的不可变计划，并保存扩展进程返回的回滚收据。
func (a *Adapter) Apply(ctx context.Context, plan proxy.Plan) (proxy.Receipt, error) {
	if plan.Adapter != adapterName {
		return proxy.Receipt{}, errors.New("plan does not belong to external adapter")
	}
	result, err := a.call(ctx, "apply", map[string]any{"plan": json.RawMessage(plan.Payload)})
	if err != nil {
		return proxy.Receipt{}, err
	}
	return proxy.Receipt{ID: plan.ID, Adapter: adapterName, Changed: true, AppliedAt: time.Now().UTC(), Payload: result}, nil
}

// Verify 要求外部进程明确返回 verified=true。
func (a *Adapter) Verify(ctx context.Context, policy proxy.DirectPolicy, receipt proxy.Receipt) error {
	result, err := a.call(ctx, "verify", map[string]any{"policy": policy, "receipt": json.RawMessage(receipt.Payload)})
	if err != nil {
		return err
	}
	var verification struct {
		Verified bool `json:"verified"`
	}
	if err := json.Unmarshal(result, &verification); err != nil {
		return err
	}
	if !verification.Verified {
		return errors.New("external adapter did not verify the policy")
	}
	return nil
}

// Rollback 将不透明收据交还同一版本外部进程撤销变更。
func (a *Adapter) Rollback(ctx context.Context, receipt proxy.Receipt) error {
	_, err := a.call(ctx, "rollback", map[string]any{"receipt": json.RawMessage(receipt.Payload)})
	return err
}

func (a *Adapter) call(ctx context.Context, method string, parameters any) (json.RawMessage, error) {
	id := fmt.Sprintf("%s-%d", method, time.Now().UnixNano())
	requestBody, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Version: protocolVersion, Method: method, Params: parameters})
	if err != nil {
		return nil, err
	}
	commandContext, cancel := context.WithTimeout(ctx, a.config.Timeout.Duration())
	defer cancel()
	command := exec.CommandContext(commandContext, a.config.Executable, a.config.Args...)
	command.Stdin = bytes.NewReader(requestBody)
	stdout := &limitedBuffer{limit: maxRPCOutput}
	stderr := &limitedBuffer{limit: maxRPCOutput}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, commandContext.Err()
		}
		return nil, fmt.Errorf("external adapter %s failed: %w", method, err)
	}
	var response rpcResponse
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(stdout.buffer.Bytes()), maxRPCOutput))
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("decode external adapter response: %w", err)
	}
	if response.JSONRPC != "2.0" || response.ID != id {
		return nil, errors.New("external adapter returned a mismatched JSON-RPC response")
	}
	if response.Error != nil {
		return nil, fmt.Errorf("external adapter error %d: %s", response.Error.Code, response.Error.Message)
	}
	if len(response.Result) == 0 || bytes.Equal(response.Result, []byte("null")) {
		return nil, errors.New("external adapter returned an empty result")
	}
	return response.Result, nil
}
