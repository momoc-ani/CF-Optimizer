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

const StateSchemaVersion = 1

// Selection 保存某个地址族当前生效的节点与稳定性状态。
type Selection struct {
	IP                  string    `json:"ip"`
	Family              int       `json:"family"`
	Score               float64   `json:"score"`
	SelectedAt          time.Time `json:"selected_at"`
	LastSuccessfulAt    time.Time `json:"last_successful_at"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
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

// State 保存后台服务当前节点、运行状态和历史摘要。
type State struct {
	Version       int          `json:"version"`
	UpdatedAt     time.Time    `json:"updated_at"`
	CurrentIPv4   *Selection   `json:"current_ipv4,omitempty"`
	CurrentIPv6   *Selection   `json:"current_ipv6,omitempty"`
	History       []RunSummary `json:"history"`
	LastError     string       `json:"last_error,omitempty"`
	LastStartedAt time.Time    `json:"last_started_at,omitempty"`
	LastEndedAt   time.Time    `json:"last_ended_at,omitempty"`
	Running       bool         `json:"running"`
}

// Store 串行化状态更新，并将每次变更原子写入磁盘。
type Store struct {
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
	s := &Store{path: filepath.Join(dataDir, "state.json"), maxRuns: maxRuns, state: State{Version: StateSchemaVersion, History: []RunSummary{}}}
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
	if s.state.Version != StateSchemaVersion {
		return nil, fmt.Errorf("unsupported state version %d", s.state.Version)
	}
	return s, nil
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
