package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
)

const (
	legacyStateSchemaVersion = 1
	// StateSchemaVersion 标识当前持久化状态结构版本。
	StateSchemaVersion       = 2
	policyTransactionVersion = 1
)

// Selection 保存某个地址族当前生效的节点与稳定性状态。
type Selection struct {
	IP                  string    `json:"ip"`
	Family              int       `json:"family"`
	Score               float64   `json:"score"`
	SelectedAt          time.Time `json:"selected_at"`
	LastSuccessfulAt    time.Time `json:"last_successful_at"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	PolicyVerified      bool      `json:"policy_verified"`
}

// ResultSummary 保存历史列表需要展示的精简测速结果。
type ResultSummary struct {
	IP         string        `json:"ip"`
	Score      float64       `json:"score"`
	AvgLatency time.Duration `json:"avg_latency"`
	Loss       float64       `json:"loss"`
	Mbps       float64       `json:"mbps"`
}

// RunSummary 保存一次完整优选任务的可审计摘要。
type RunSummary struct {
	ID           string          `json:"id"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   time.Time       `json:"finished_at"`
	Candidates   int             `json:"candidates"`
	Qualified    int             `json:"qualified"`
	Best         []ResultSummary `json:"best,omitempty"`
	SelectedIPv4 string          `json:"selected_ipv4,omitempty"`
	SelectedIPv6 string          `json:"selected_ipv6,omitempty"`
	SwitchReason string          `json:"switch_reason,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// NodeStats 聚合节点历史表现，并记录连续失败后的冷却时间。
type NodeStats struct {
	Attempts      int       `json:"attempts"`
	Successes     int       `json:"successes"`
	FailureStreak int       `json:"failure_streak"`
	AverageScore  float64   `json:"average_score"`
	LastTestedAt  time.Time `json:"last_tested_at"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
}

// DomainDiscovery 保存本机观察到的精确域名及其 Cloudflare 和预检状态。
type DomainDiscovery struct {
	Domain                string    `json:"domain"`
	Source                string    `json:"source"`
	FirstSeenAt           time.Time `json:"first_seen_at"`
	LastSeenAt            time.Time `json:"last_seen_at"`
	CloudflareVerified    bool      `json:"cloudflare_verified"`
	PreflightVerified     bool      `json:"preflight_verified"`
	Active                bool      `json:"active"`
	LastResolvedAddresses []string  `json:"last_resolved_addresses,omitempty"`
	LastError             string    `json:"last_error,omitempty"`
}

// State 保存后台服务当前节点、运行状态和历史摘要。
type State struct {
	Version           int                        `json:"version"`
	UpdatedAt         time.Time                  `json:"updated_at"`
	CurrentIPv4       *Selection                 `json:"current_ipv4,omitempty"`
	CurrentIPv6       *Selection                 `json:"current_ipv6,omitempty"`
	History           []RunSummary               `json:"history"`
	Nodes             map[string]NodeStats       `json:"nodes"`
	DiscoveredDomains map[string]DomainDiscovery `json:"discovered_domains"`
	Policy            *PolicySnapshot            `json:"policy,omitempty"`
	PendingPolicy     *PolicyTransaction         `json:"pending_policy,omitempty"`
	LastError         string                     `json:"last_error,omitempty"`
	LastStartedAt     time.Time                  `json:"last_started_at,omitempty"`
	LastEndedAt       time.Time                  `json:"last_ended_at,omitempty"`
	Running           bool                       `json:"running"`
}

// PolicyTransaction 持久化尚未提交的适配器收据，供崩溃恢复和安全卸载使用。
type PolicyTransaction struct {
	Version   int             `json:"version"`
	StartedAt time.Time       `json:"started_at"`
	Policy    json.RawMessage `json:"policy"`
	Receipts  json.RawMessage `json:"receipts"`
}

// PolicySnapshot 保存最后一次已验证策略及适配器回滚收据。
type PolicySnapshot struct {
	IPv4CIDRs      []string                `json:"ipv4_cidrs"`
	IPv6CIDRs      []string                `json:"ipv6_cidrs"`
	Domains        []string                `json:"domains"`
	DomainMappings []DomainMappingSnapshot `json:"domain_mappings,omitempty"`
	Processes      []string                `json:"processes"`
	Receipts       json.RawMessage         `json:"receipts"`
	AppliedAt      time.Time               `json:"applied_at"`
}

// DomainMappingSnapshot 保存已提交策略中的精确域名与地址映射。
type DomainMappingSnapshot struct {
	Domain    string   `json:"domain"`
	Addresses []string `json:"addresses"`
}

// Store 串行化状态更新，并将每次变更原子写入磁盘。
type Store struct {
	dataDir string
	path    string
	maxRuns int
	mu      sync.RWMutex
	state   State
}

// Open 加载状态文件；文件不存在时返回空的初始状态。
func Open(dataDir string, maxRuns int) (*Store, error) {
	if maxRuns < 1 {
		maxRuns = 500
	}
	s := &Store{dataDir: dataDir, path: filepath.Join(dataDir, "state.json"), maxRuns: maxRuns, state: State{Version: StateSchemaVersion, History: []RunSummary{}, Nodes: map[string]NodeStats{}, DiscoveredDomains: map[string]DomainDiscovery{}}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	if s.state.Version == legacyStateSchemaVersion {
		s.state.Version = StateSchemaVersion
	}
	if s.state.Version != StateSchemaVersion {
		return nil, fmt.Errorf("unsupported state version %d", s.state.Version)
	}
	if s.state.PendingPolicy != nil && s.state.PendingPolicy.Version != policyTransactionVersion {
		return nil, fmt.Errorf("unsupported pending policy transaction version %d", s.state.PendingPolicy.Version)
	}
	if s.state.Nodes == nil {
		s.state.Nodes = map[string]NodeStats{}
	}
	if s.state.DiscoveredDomains == nil {
		s.state.DiscoveredDomains = map[string]DomainDiscovery{}
	}
	return s, nil
}

// NewPolicyTransaction 创建当前状态格式支持的待恢复策略事务。
func NewPolicyTransaction(startedAt time.Time, policy, receipts json.RawMessage) *PolicyTransaction {
	return &PolicyTransaction{Version: policyTransactionVersion, StartedAt: startedAt.UTC(), Policy: policy, Receipts: receipts}
}

// SaveRunDetail 保存一次运行的候选明细，并清理超过保留期限的旧文件。
func (s *Store) SaveRunDetail(runID string, payload json.RawMessage, retention time.Duration) error {
	if !validRunID(runID) {
		return errors.New("invalid run ID")
	}
	directory := filepath.Join(s.dataDir, "run-details")
	document := struct {
		Version int             `json:"version"`
		RunID   string          `json:"run_id"`
		SavedAt time.Time       `json:"saved_at"`
		Payload json.RawMessage `json:"payload"`
	}{Version: 1, RunID: runID, SavedAt: time.Now().UTC(), Payload: payload}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(directory, runID+".json"), append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-retention)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		fileInfo, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if fileInfo.ModTime().Before(cutoff) {
			if removeErr := os.Remove(filepath.Join(directory, entry.Name())); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return removeErr
			}
		}
	}
	return nil
}

func validRunID(runID string) bool {
	if runID == "" || len(runID) > 128 {
		return false
	}
	for _, character := range runID {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

// Snapshot 返回可由调用方安全读取的状态副本。
func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, _ := json.Marshal(s.state)
	var clone State
	_ = json.Unmarshal(data, &clone)
	return clone
}

// Update 在持有写锁时应用变更，并仅在持久化成功后发布新状态。
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state
	if err := fn(&next); err != nil {
		return err
	}
	next.Version = StateSchemaVersion
	if next.Nodes == nil {
		next.Nodes = map[string]NodeStats{}
	}
	if next.DiscoveredDomains == nil {
		next.DiscoveredDomains = map[string]DomainDiscovery{}
	}
	next.UpdatedAt = time.Now().UTC()
	if len(next.History) > s.maxRuns {
		next.History = append([]RunSummary(nil), next.History[len(next.History)-s.maxRuns:]...)
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := fsutil.WriteFileAtomic(s.path, data, 0o600); err != nil {
		return fmt.Errorf("persist state: %w", err)
	}
	s.state = next
	return nil
}
