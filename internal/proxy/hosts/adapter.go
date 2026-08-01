package hosts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

const (
	adapterName = "windows-hosts"
	beginMarker = "# BEGIN CF Optimizer managed block"
	endMarker   = "# END CF Optimizer managed block"
	missingHash = "missing"
)

type planPayload struct {
	Content      []byte `json:"content"`
	ExpectedHash string `json:"expected_hash"`
	EntryCount   int    `json:"entry_count"`
}

type receiptPayload struct {
	Previous             []byte `json:"previous"`
	AppliedHash          string `json:"applied_hash"`
	BackupPreviousExists bool   `json:"backup_previous_exists"`
	BackupPrevious       []byte `json:"backup_previous,omitempty"`
	BackupAppliedHash    string `json:"backup_applied_hash,omitempty"`
}

// Adapter 只替换带标记的 Windows Hosts 区块，并为每次修改保留备份。
type Adapter struct {
	config config.HostsConfig
}

// New 创建受管 Hosts 适配器。
func New(cfg config.HostsConfig) (*Adapter, error) {
	if cfg.Path == "" {
		return nil, errors.New("Hosts path is required")
	}
	return &Adapter{config: cfg}, nil
}

// Name 返回稳定的适配器标识。
func (a *Adapter) Name() string { return adapterName }

// Capabilities 声明 Hosts 可表达 IPv4、IPv6 与域名映射并支持回滚。
func (a *Adapter) Capabilities() proxy.Capabilities {
	return proxy.Capabilities{DomainMappings: true, Rollback: true}
}

// Detect 确认目标 Hosts 文件存在且可读取。
func (a *Adapter) Detect(context.Context) (proxy.Detection, error) {
	if _, err := os.Stat(a.config.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return proxy.Detection{Present: false, Message: "Hosts file does not exist"}, nil
		}
		return proxy.Detection{}, err
	}
	return proxy.Detection{Present: true, Message: "managed Hosts block is available"}, nil
}

// Plan 保留全部非受管内容，并生成确定性地址与域名映射区块。
func (a *Adapter) Plan(_ context.Context, policy proxy.DirectPolicy) (proxy.Plan, error) {
	previous, err := os.ReadFile(a.config.Path)
	if err != nil {
		return proxy.Plan{}, err
	}
	base, newline, err := removeManagedBlock(previous)
	if err != nil {
		return proxy.Plan{}, err
	}
	var lines []string
	for _, mapping := range policy.DomainMappings {
		for _, address := range mapping.Addresses {
			lines = append(lines, address+" "+mapping.Domain)
		}
	}
	if len(lines) == 0 {
		return proxy.Plan{}, errors.New("Hosts policy has no domain mapping")
	}
	content := append([]byte(nil), base...)
	if len(content) > 0 && !bytes.HasSuffix(content, []byte(newline)) {
		content = append(content, newline...)
	}
	block := beginMarker + newline + strings.Join(lines, newline) + newline + endMarker + newline
	content = append(content, block...)
	payload, err := json.Marshal(planPayload{Content: content, ExpectedHash: hash(previous), EntryCount: len(lines)})
	if err != nil {
		return proxy.Plan{}, err
	}
	return proxy.Plan{
		ID: fmt.Sprintf("hosts-%d", time.Now().UnixNano()), Adapter: adapterName, Policy: policy,
		Summary: []string{fmt.Sprintf("replace %d entries in the managed Hosts block", len(lines))}, Payload: payload,
	}, nil
}

// Apply 检查并发修改、写入备份并原子替换 Hosts 文件。
func (a *Adapter) Apply(_ context.Context, plan proxy.Plan) (proxy.Receipt, error) {
	if plan.Adapter != adapterName {
		return proxy.Receipt{}, errors.New("plan does not belong to Hosts adapter")
	}
	var payload planPayload
	if err := json.Unmarshal(plan.Payload, &payload); err != nil {
		return proxy.Receipt{}, err
	}
	previous, err := os.ReadFile(a.config.Path)
	if err != nil {
		return proxy.Receipt{}, err
	}
	if hash(previous) != payload.ExpectedHash {
		return proxy.Receipt{}, errors.New("Hosts file changed after planning")
	}
	changed := !bytes.Equal(previous, payload.Content)
	backupPath := a.config.Path + ".cf-optimizer.backup"
	backupPrevious, backupExisted, err := readOptionalFile(backupPath)
	if err != nil {
		return proxy.Receipt{}, fmt.Errorf("read Hosts backup: %w", err)
	}
	if changed {
		if err := fsutil.WriteFileAtomic(backupPath, previous, 0o600); err != nil {
			return proxy.Receipt{}, fmt.Errorf("back up Hosts file: %w", err)
		}
		if err := writeHostsFile(a.config.Path, payload.Content, previous, 0o644); err != nil {
			return proxy.Receipt{}, fmt.Errorf("write Hosts file: %w", err)
		}
	}
	receiptData, err := json.Marshal(receiptPayload{
		Previous: previous, AppliedHash: hash(payload.Content), BackupPreviousExists: backupExisted,
		BackupPrevious: backupPrevious, BackupAppliedHash: hash(previous),
	})
	if err != nil {
		return proxy.Receipt{}, err
	}
	return proxy.Receipt{ID: plan.ID, Adapter: adapterName, Changed: changed, AppliedAt: time.Now().UTC(), Payload: receiptData}, nil
}

// Verify 确认 Hosts 文件仍是本次应用的完整版本。
func (a *Adapter) Verify(_ context.Context, _ proxy.DirectPolicy, receipt proxy.Receipt) error {
	var payload receiptPayload
	if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
		return err
	}
	content, err := os.ReadFile(a.config.Path)
	if err != nil {
		return err
	}
	if hash(content) != payload.AppliedHash {
		return errors.New("Hosts verification failed")
	}
	return nil
}

// Rollback 在 Hosts 未被外部修改时恢复应用前的完整文件。
func (a *Adapter) Rollback(_ context.Context, receipt proxy.Receipt) error {
	if !receipt.Changed {
		return nil
	}
	var payload receiptPayload
	if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
		return err
	}
	current, err := os.ReadFile(a.config.Path)
	if err != nil {
		return err
	}
	hostsRestored := bytes.Equal(current, payload.Previous)
	if !hostsRestored && hash(current) != payload.AppliedHash {
		return errors.New("Hosts changed after apply; refusing rollback overwrite")
	}
	if payload.BackupAppliedHash == "" {
		if hostsRestored {
			return nil
		}
		return writeHostsFile(a.config.Path, payload.Previous, current, 0o644)
	}
	backupPath := a.config.Path + ".cf-optimizer.backup"
	backupCurrent, backupExists, err := readOptionalFile(backupPath)
	if err != nil {
		return err
	}
	backupRestored := optionalFileEquals(backupCurrent, backupExists, payload.BackupPrevious, payload.BackupPreviousExists)
	if !backupRestored && (!backupExists || hash(backupCurrent) != payload.BackupAppliedHash) {
		return errors.New("Hosts backup changed after apply; refusing rollback overwrite")
	}
	if !hostsRestored {
		if err := writeHostsFile(a.config.Path, payload.Previous, current, 0o644); err != nil {
			return err
		}
	}
	if backupRestored {
		return nil
	}
	return restoreOptionalFile(backupPath, payload.BackupPrevious, payload.BackupPreviousExists, 0o600)
}

// readOptionalFile 区分不存在的可选文件与实际读取失败。
func readOptionalFile(path string) ([]byte, bool, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return content, err == nil, err
}

// restoreOptionalFile 按收据恢复旧内容，或移除本次新建的受管文件。
func restoreOptionalFile(path string, content []byte, existed bool, permission os.FileMode) error {
	if existed {
		return fsutil.WriteFileAtomic(path, content, permission)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// optionalFileEquals 同时比较可选文件的存在状态和内容。
func optionalFileEquals(current []byte, currentExists bool, previous []byte, previousExists bool) bool {
	return currentExists == previousExists && (!currentExists || bytes.Equal(current, previous))
}

func removeManagedBlock(content []byte) ([]byte, string, error) {
	newline := "\n"
	if bytes.Contains(content, []byte("\r\n")) {
		newline = "\r\n"
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	var result []string
	inBlock := false
	foundBegin := false
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case beginMarker:
			if inBlock || foundBegin {
				return nil, newline, errors.New("Hosts contains duplicate managed block markers")
			}
			inBlock = true
			foundBegin = true
		case endMarker:
			if !inBlock {
				return nil, newline, errors.New("Hosts contains an unmatched managed block end marker")
			}
			inBlock = false
		default:
			if !inBlock {
				result = append(result, line)
			}
		}
	}
	if inBlock {
		return nil, newline, errors.New("Hosts contains an unterminated managed block")
	}
	for len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	return []byte(strings.Join(result, newline)), newline, nil
}

func hash(content []byte) string {
	if content == nil {
		return missingHash
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
