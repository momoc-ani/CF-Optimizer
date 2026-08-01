package managedfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

const missingHash = "missing"

// Formatter 将统一策略转换为代理内核可验证的独立配置片段。
type Formatter func(proxy.DirectPolicy, string) ([]byte, int, error)

type planPayload struct {
	Content      []byte `json:"content"`
	ExpectedHash string `json:"expected_hash"`
	RuleCount    int    `json:"rule_count"`
}

type receiptPayload struct {
	PreviousExists bool   `json:"previous_exists"`
	Previous       []byte `json:"previous"`
	AppliedHash    string `json:"applied_hash"`
}

// Adapter 为只管理独立配置片段的代理内核提供并发检查、命令验证和回滚。
type Adapter struct {
	name         string
	config       config.ManagedProxyConfig
	formatter    Formatter
	capabilities proxy.Capabilities
}

// New 创建受管配置文件适配器。
func New(name string, cfg config.ManagedProxyConfig, formatter Formatter, capabilities proxy.Capabilities) (*Adapter, error) {
	if name == "" || cfg.ManagedFile == "" || formatter == nil {
		return nil, errors.New("managed adapter name, file and formatter are required")
	}
	return &Adapter{name: name, config: cfg, formatter: formatter, capabilities: capabilities}, nil
}

// Name 返回代理内核名称。
func (a *Adapter) Name() string { return a.name }

// Capabilities 返回该配置片段格式支持的策略类型。
func (a *Adapter) Capabilities() proxy.Capabilities { return a.capabilities }

// Detect 检查受管路径以及可选验证/重载程序是否可用。
func (a *Adapter) Detect(context.Context) (proxy.Detection, error) {
	if a.config.Executable != "" {
		path, err := exec.LookPath(a.config.Executable)
		if err != nil {
			return proxy.Detection{Present: false, Message: "configured executable is unavailable"}, nil
		}
		return proxy.Detection{Present: true, Version: filepath.Base(path), Message: "managed fragment and executable are available"}, nil
	}
	return proxy.Detection{Present: true, Message: "managed fragment mode; reload is delegated to the proxy client"}, nil
}

// Plan 生成受管片段，并记录当前内容哈希用于应用前并发校验。
func (a *Adapter) Plan(_ context.Context, policy proxy.DirectPolicy) (proxy.Plan, error) {
	content, ruleCount, err := a.formatter(policy, a.config.DirectOutbound)
	if err != nil {
		return proxy.Plan{}, err
	}
	if ruleCount == 0 {
		return proxy.Plan{}, fmt.Errorf("%s policy contains no supported rule", a.name)
	}
	previous, existed, err := readOptional(a.config.ManagedFile)
	if err != nil {
		return proxy.Plan{}, err
	}
	expectedHash := missingHash
	if existed {
		expectedHash = hash(previous)
	}
	payload, err := json.Marshal(planPayload{Content: content, ExpectedHash: expectedHash, RuleCount: ruleCount})
	if err != nil {
		return proxy.Plan{}, err
	}
	return proxy.Plan{
		ID: fmt.Sprintf("%s-%d", a.name, time.Now().UnixNano()), Adapter: a.name, Policy: policy,
		Summary: []string{fmt.Sprintf("write and validate %d managed DIRECT rules", ruleCount)}, Payload: payload,
	}, nil
}

// Apply 原子写入片段，依次执行验证和重载；失败时恢复原文件。
func (a *Adapter) Apply(ctx context.Context, plan proxy.Plan) (proxy.Receipt, error) {
	if plan.Adapter != a.name {
		return proxy.Receipt{}, fmt.Errorf("plan does not belong to %s adapter", a.name)
	}
	var payload planPayload
	if err := json.Unmarshal(plan.Payload, &payload); err != nil {
		return proxy.Receipt{}, err
	}
	previous, existed, err := readOptional(a.config.ManagedFile)
	if err != nil {
		return proxy.Receipt{}, err
	}
	actualHash := missingHash
	if existed {
		actualHash = hash(previous)
	}
	if actualHash != payload.ExpectedHash {
		return proxy.Receipt{}, fmt.Errorf("%s managed file changed after planning", a.name)
	}
	changed := !existed || !bytes.Equal(previous, payload.Content)
	if changed {
		if err := fsutil.WriteFileAtomic(a.config.ManagedFile, payload.Content, 0o640); err != nil {
			return proxy.Receipt{}, err
		}
		if err := a.runConfigured(ctx, a.config.ValidateArgs); err != nil {
			_ = restoreOptional(a.config.ManagedFile, previous, existed)
			return proxy.Receipt{}, fmt.Errorf("validate %s managed file: %w", a.name, err)
		}
		if err := a.runConfigured(ctx, a.config.ReloadArgs); err != nil {
			_ = restoreOptional(a.config.ManagedFile, previous, existed)
			_ = a.runConfigured(ctx, a.config.ReloadArgs)
			return proxy.Receipt{}, fmt.Errorf("reload %s: %w", a.name, err)
		}
	}
	receiptData, err := json.Marshal(receiptPayload{PreviousExists: existed, Previous: previous, AppliedHash: hash(payload.Content)})
	if err != nil {
		return proxy.Receipt{}, err
	}
	return proxy.Receipt{ID: plan.ID, Adapter: a.name, Changed: changed, AppliedAt: time.Now().UTC(), Payload: receiptData}, nil
}

// Verify 确认受管文件仍与已应用收据一致。
func (a *Adapter) Verify(_ context.Context, _ proxy.DirectPolicy, receipt proxy.Receipt) error {
	var payload receiptPayload
	if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
		return err
	}
	content, exists, err := readOptional(a.config.ManagedFile)
	if err != nil {
		return err
	}
	if !exists || hash(content) != payload.AppliedHash {
		return fmt.Errorf("%s managed file verification failed", a.name)
	}
	return nil
}

// Rollback 在文件未被外部修改时恢复原片段，并执行受控重载。
func (a *Adapter) Rollback(ctx context.Context, receipt proxy.Receipt) error {
	if !receipt.Changed {
		return nil
	}
	var payload receiptPayload
	if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
		return err
	}
	content, exists, err := readOptional(a.config.ManagedFile)
	if err != nil {
		return err
	}
	if !exists || hash(content) != payload.AppliedHash {
		return fmt.Errorf("%s managed file changed after apply; refusing rollback overwrite", a.name)
	}
	if err := restoreOptional(a.config.ManagedFile, payload.Previous, payload.PreviousExists); err != nil {
		return err
	}
	return a.runConfigured(ctx, a.config.ReloadArgs)
}

func (a *Adapter) runConfigured(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		return nil
	}
	if a.config.Executable == "" {
		return errors.New("adapter executable is not configured")
	}
	directory := filepath.Dir(a.config.ManagedFile)
	expanded := make([]string, len(arguments))
	for index, argument := range arguments {
		argument = strings.ReplaceAll(argument, "{{dir}}", directory)
		argument = strings.ReplaceAll(argument, "{{file}}", a.config.ManagedFile)
		expanded[index] = argument
	}
	commandContext, cancel := context.WithTimeout(ctx, a.config.Timeout.Duration())
	defer cancel()
	if _, err := exec.CommandContext(commandContext, a.config.Executable, expanded...).Output(); err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return commandContext.Err()
		}
		return err
	}
	return nil
}

func readOptional(path string) ([]byte, bool, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}

func restoreOptional(path string, content []byte, existed bool) error {
	if existed {
		return fsutil.WriteFileAtomic(path, content, 0o640)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func hash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
