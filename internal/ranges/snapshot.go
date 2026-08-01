package ranges

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
)

const SnapshotVersion = 1

var builtinIPv4 = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
}

var builtinIPv6 = []string{
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

// Snapshot 保存经过校验的 Cloudflare 网段及来源元数据。
type Snapshot struct {
	Version   int       `json:"version"`
	FetchedAt time.Time `json:"fetched_at"`
	Source    string    `json:"source"`
	ETag      string    `json:"etag,omitempty"`
	Hash      string    `json:"hash"`
	IPv4      []string  `json:"ipv4"`
	IPv6      []string  `json:"ipv6"`
	Include   []string  `json:"include,omitempty"`
	Exclude   []string  `json:"exclude,omitempty"`
}

// UpdateResult 描述网段刷新结果及显式降级警告。
type UpdateResult struct {
	Snapshot Snapshot `json:"snapshot"`
	Updated  bool     `json:"updated"`
	Warning  string   `json:"warning,omitempty"`
}

// Catalog 负责网段获取、校验、缓存和回退。
type Catalog struct {
	config  config.RangesConfig
	dataDir string
	client  *http.Client
	now     func() time.Time
}

// NewCatalog 创建不读取系统代理环境的官方网段目录。
func NewCatalog(cfg config.RangesConfig, dataDir string) *Catalog {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Catalog{
		config: cfg, dataDir: dataDir, now: time.Now,
		client: &http.Client{Transport: transport, Timeout: cfg.RequestTimeout.Duration()},
	}
}

// CurrentPath 返回当前网段快照路径。
func (m *Catalog) CurrentPath() string { return filepath.Join(m.dataDir, "ranges.json") }

// PreviousPath 返回上一份有效网段快照路径。
func (m *Catalog) PreviousPath() string { return filepath.Join(m.dataDir, "ranges.previous.json") }

// Builtin 返回离线首次启动可用的内置网段快照。
func Builtin() Snapshot {
	s := Snapshot{Version: SnapshotVersion, Source: "builtin", IPv4: append([]string(nil), builtinIPv4...), IPv6: append([]string(nil), builtinIPv6...)}
	s.Hash = calculateHash(s.IPv4, s.IPv6)
	return s
}

// Load 读取当前缓存，损坏时回退上一版本，不存在时使用内置快照。
func (m *Catalog) Load() (Snapshot, error) {
	snapshot, err := readSnapshot(m.CurrentPath())
	if errors.Is(err, os.ErrNotExist) {
		snapshot = Builtin()
	} else if err != nil {
		previous, previousErr := readSnapshot(m.PreviousPath())
		if previousErr != nil {
			return Snapshot{}, fmt.Errorf("load range cache: %w", err)
		}
		snapshot = previous
	}
	return m.withOverrides(snapshot)
}

// Update 按刷新周期请求官方数据，拒绝异常变更并保留最后有效快照。
func (m *Catalog) Update(ctx context.Context, force bool) (UpdateResult, error) {
	current, loadErr := m.Load()
	if loadErr == nil && !force && !current.FetchedAt.IsZero() && m.now().Sub(current.FetchedAt) < m.config.RefreshInterval.Duration() {
		return UpdateResult{Snapshot: current}, nil
	}

	fresh, notModified, err := m.fetchAPI(ctx, current.ETag)
	if err != nil {
		fresh, err = m.fetchFallback(ctx)
	}
	if notModified && loadErr == nil {
		current.FetchedAt = m.now().UTC()
		if err := m.persist(current); err != nil {
			return UpdateResult{}, err
		}
		return UpdateResult{Snapshot: current}, nil
	}
	if err != nil {
		if loadErr == nil {
			return UpdateResult{Snapshot: current, Warning: fmt.Sprintf("range refresh failed; using cached snapshot: %v", err)}, nil
		}
		fallback := Builtin()
		fallback, overrideErr := m.withOverrides(fallback)
		if overrideErr != nil {
			return UpdateResult{}, overrideErr
		}
		return UpdateResult{Snapshot: fallback, Warning: fmt.Sprintf("range refresh failed; using built-in snapshot: %v", err)}, nil
	}

	if err := validateRemote(fresh); err != nil {
		if loadErr == nil {
			return UpdateResult{Snapshot: current, Warning: fmt.Sprintf("range update rejected; using cached snapshot: %v", err)}, nil
		}
		return UpdateResult{}, err
	}
	if loadErr == nil && current.Source != "builtin" {
		change := changePercent(current, fresh)
		if change > m.config.MaxChangePercent {
			return UpdateResult{Snapshot: current, Warning: fmt.Sprintf("range update rejected: %.1f%% change exceeds %.1f%% limit", change, m.config.MaxChangePercent)}, nil
		}
	}
	fresh.FetchedAt = m.now().UTC()
	fresh.Version = SnapshotVersion
	fresh.Hash = calculateHash(fresh.IPv4, fresh.IPv6)
	if err := m.persist(fresh); err != nil {
		return UpdateResult{}, err
	}
	withOverrides, err := m.withOverrides(fresh)
	if err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Snapshot: withOverrides, Updated: fresh.Hash != current.Hash}, nil
}

func (m *Catalog) fetchAPI(ctx context.Context, etag string) (Snapshot, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.config.APIURL, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return Snapshot{}, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, false, fmt.Errorf("Cloudflare API returned %s", resp.Status)
	}
	var envelope struct {
		Success bool `json:"success"`
		Result  struct {
			IPv4 []string `json:"ipv4_cidrs"`
			IPv6 []string `json:"ipv6_cidrs"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return Snapshot{}, false, fmt.Errorf("decode Cloudflare API response: %w", err)
	}
	if !envelope.Success {
		return Snapshot{}, false, errors.New("Cloudflare API reported failure")
	}
	return newSnapshot("cloudflare-api", resp.Header.Get("ETag"), envelope.Result.IPv4, envelope.Result.IPv6), false, nil
}

func (m *Catalog) fetchFallback(ctx context.Context) (Snapshot, error) {
	v4, err4 := m.fetchList(ctx, m.config.IPv4URL)
	v6, err6 := m.fetchList(ctx, m.config.IPv6URL)
	if err4 != nil || err6 != nil {
		return Snapshot{}, fmt.Errorf("fallback endpoints failed (IPv4: %v; IPv6: %v)", err4, err6)
	}
	return newSnapshot("cloudflare-text", "", v4, v6), nil
}

func (m *Catalog) fetchList(ctx context.Context, endpoint string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(data)), nil
}

func newSnapshot(source, etag string, ipv4, ipv6 []string) Snapshot {
	return Snapshot{Version: SnapshotVersion, Source: source, ETag: etag, IPv4: normalizeStrings(ipv4), IPv6: normalizeStrings(ipv6)}
}

func (m *Catalog) withOverrides(snapshot Snapshot) (Snapshot, error) {
	result := snapshot
	result.Include = normalizeStrings(m.config.Include)
	result.Exclude = normalizeStrings(m.config.Exclude)
	for _, raw := range result.Include {
		prefix, err := parsePublicPrefix(raw)
		if err != nil {
			return Snapshot{}, fmt.Errorf("invalid included range %q: %w", raw, err)
		}
		if prefix.Addr().Is4() {
			result.IPv4 = append(result.IPv4, prefix.String())
		} else {
			result.IPv6 = append(result.IPv6, prefix.String())
		}
	}
	for _, raw := range result.Exclude {
		if _, err := netip.ParsePrefix(raw); err != nil {
			return Snapshot{}, fmt.Errorf("invalid excluded range %q: %w", raw, err)
		}
	}
	result.IPv4 = normalizeStrings(result.IPv4)
	result.IPv6 = normalizeStrings(result.IPv6)
	return result, nil
}

func (m *Catalog) persist(snapshot Snapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := os.Stat(m.CurrentPath()); err == nil {
		if err := fsutil.CopyFileAtomic(m.CurrentPath(), m.PreviousPath(), 0o600); err != nil {
			return fmt.Errorf("back up range cache: %w", err)
		}
	}
	if err := fsutil.WriteFileAtomic(m.CurrentPath(), data, 0o600); err != nil {
		return fmt.Errorf("write range cache: %w", err)
	}
	return nil
}

func readSnapshot(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Version != SnapshotVersion {
		return Snapshot{}, fmt.Errorf("unsupported range snapshot version %d", snapshot.Version)
	}
	if err := validateSnapshot(snapshot, 1, 1); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Hash != calculateHash(snapshot.IPv4, snapshot.IPv6) {
		return Snapshot{}, errors.New("range snapshot hash mismatch")
	}
	return snapshot, nil
}

func validateRemote(snapshot Snapshot) error { return validateSnapshot(snapshot, 5, 2) }

func validateSnapshot(snapshot Snapshot, minIPv4, minIPv6 int) error {
	if len(snapshot.IPv4) < minIPv4 || len(snapshot.IPv4) > 1000 {
		return fmt.Errorf("unexpected IPv4 range count %d", len(snapshot.IPv4))
	}
	if len(snapshot.IPv6) < minIPv6 || len(snapshot.IPv6) > 1000 {
		return fmt.Errorf("unexpected IPv6 range count %d", len(snapshot.IPv6))
	}
	for _, raw := range append(append([]string{}, snapshot.IPv4...), snapshot.IPv6...) {
		if _, err := parsePublicPrefix(raw); err != nil {
			return fmt.Errorf("unsafe range %q: %w", raw, err)
		}
	}
	return nil
}

func parsePublicPrefix(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, err
	}
	prefix = prefix.Masked()
	addr := prefix.Addr()
	if prefix.Bits() == 0 || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() {
		return netip.Prefix{}, errors.New("range is not public unicast")
	}
	return prefix, nil
}

// Prefixes 将启用地址族的字符串网段解析为不可变前缀列表。
func (s Snapshot) Prefixes(ipv4, ipv6 bool) ([]netip.Prefix, error) {
	raw := make([]string, 0, len(s.IPv4)+len(s.IPv6))
	if ipv4 {
		raw = append(raw, s.IPv4...)
	}
	if ipv6 {
		raw = append(raw, s.IPv6...)
	}
	result := make([]netip.Prefix, 0, len(raw))
	for _, value := range raw {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, err
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}

// ExcludedPrefixes 返回已成功解析的用户排除网段。
func (s Snapshot) ExcludedPrefixes() []netip.Prefix {
	result := make([]netip.Prefix, 0, len(s.Exclude))
	for _, raw := range s.Exclude {
		if prefix, err := netip.ParsePrefix(raw); err == nil {
			result = append(result, prefix.Masked())
		}
	}
	return result
}

// Contains 报告地址是否属于当前有效 Cloudflare 网段且未命中排除项。
func (s Snapshot) Contains(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	for _, excluded := range s.ExcludedPrefixes() {
		if excluded.Contains(address) {
			return false
		}
	}
	rawPrefixes := s.IPv6
	if address.Is4() {
		rawPrefixes = s.IPv4
	}
	for _, raw := range rawPrefixes {
		if prefix, err := netip.ParsePrefix(raw); err == nil && prefix.Contains(address) {
			return true
		}
	}
	return false
}

func normalizeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			value = prefix.Masked().String()
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func calculateHash(ipv4, ipv6 []string) string {
	v4 := normalizeStrings(ipv4)
	v6 := normalizeStrings(ipv6)
	h := sha256.New()
	_, _ = io.WriteString(h, strings.Join(v4, "\n"))
	_, _ = io.WriteString(h, "\n--ipv6--\n")
	_, _ = io.WriteString(h, strings.Join(v6, "\n"))
	return hex.EncodeToString(h.Sum(nil))
}

func changePercent(old, next Snapshot) float64 {
	a := append(append([]string{}, old.IPv4...), old.IPv6...)
	b := append(append([]string{}, next.IPv4...), next.IPv6...)
	oldSet := make(map[string]struct{}, len(a))
	for _, value := range a {
		oldSet[value] = struct{}{}
	}
	changes := 0
	for _, value := range b {
		if _, ok := oldSet[value]; ok {
			delete(oldSet, value)
		} else {
			changes++
		}
	}
	changes += len(oldSet)
	base := len(a)
	if base == 0 {
		return 100
	}
	return float64(changes) * 100 / float64(base)
}
