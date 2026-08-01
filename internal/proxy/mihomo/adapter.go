package mihomo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"gopkg.in/yaml.v3"
)

const (
	adapterName       = "mihomo"
	managedFileHeader = "# Managed by CF Optimizer. Manual changes will be replaced.\n"
	missingFileHash   = "missing"
	maxAPIResponse    = 8 << 20
)

type planPayload struct {
	Content      []byte   `json:"content"`
	ExpectedHash string   `json:"expected_hash"`
	Rules        []string `json:"rules"`
}

type receiptPayload struct {
	PreviousExists bool     `json:"previous_exists"`
	Previous       []byte   `json:"previous"`
	AppliedHash    string   `json:"applied_hash"`
	Rules          []string `json:"rules"`
}

// Adapter 管理独立 Mihomo rule-provider 文件，并通过控制 API 重载和验证。
type Adapter struct {
	config     config.MihomoConfig
	controller *url.URL
	client     *http.Client
}

// New 创建强制禁用系统代理的 Mihomo 控制 API 客户端。
func New(cfg config.MihomoConfig) (*Adapter, error) {
	controller, err := url.Parse(cfg.Controller)
	if err != nil || controller.Host == "" {
		return nil, errors.New("invalid Mihomo controller URL")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Adapter{config: cfg, controller: controller, client: &http.Client{Transport: transport, Timeout: cfg.Timeout.Duration()}}, nil
}

// Name 返回稳定的适配器标识。
func (a *Adapter) Name() string { return adapterName }

// Capabilities 返回 Mihomo 受管规则支持的策略类型。
func (a *Adapter) Capabilities() proxy.Capabilities {
	return proxy.Capabilities{Processes: true, IPv4: true, IPv6: true, Domains: true, HotReload: a.config.ReloadConfig != "", Rollback: true}
}

// Detect 读取控制端版本，不把认证信息写入结果。
func (a *Adapter) Detect(ctx context.Context) (proxy.Detection, error) {
	body, status, err := a.request(ctx, http.MethodGet, "/version", nil)
	if err != nil {
		return proxy.Detection{Present: false}, err
	}
	if status != http.StatusOK {
		return proxy.Detection{Present: false}, fmt.Errorf("Mihomo version endpoint returned %d", status)
	}
	var version struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &version); err != nil {
		return proxy.Detection{Present: true}, fmt.Errorf("decode Mihomo version: %w", err)
	}
	return proxy.Detection{Present: true, Version: version.Version, Message: "controller API is reachable"}, nil
}

// Plan 生成完整受管 provider 内容，并记录当前文件哈希用于并发修改检查。
func (a *Adapter) Plan(_ context.Context, policy proxy.DirectPolicy) (proxy.Plan, error) {
	rules := rulesForPolicy(policy)
	if len(rules) == 0 {
		return proxy.Plan{}, errors.New("Mihomo policy contains no supported rule")
	}
	encoded, err := yaml.Marshal(struct {
		Payload []string `yaml:"payload"`
	}{Payload: rules})
	if err != nil {
		return proxy.Plan{}, err
	}
	content := append([]byte(managedFileHeader), encoded...)
	existing, exists, err := readOptionalFile(a.config.ProviderFile)
	if err != nil {
		return proxy.Plan{}, err
	}
	expectedHash := missingFileHash
	if exists {
		expectedHash = contentHash(existing)
	}
	payload, err := json.Marshal(planPayload{Content: content, ExpectedHash: expectedHash, Rules: rules})
	if err != nil {
		return proxy.Plan{}, err
	}
	return proxy.Plan{
		ID: fmt.Sprintf("mihomo-%d", time.Now().UnixNano()), Adapter: adapterName, Policy: policy,
		Summary: []string{fmt.Sprintf("write %d managed DIRECT rules", len(rules)), "reload configured Mihomo profile", "verify active rules through controller API"},
		Payload: payload,
	}, nil
}

// Apply 检查文件未被并发修改后原子写入，并按配置触发热重载。
func (a *Adapter) Apply(ctx context.Context, plan proxy.Plan) (proxy.Receipt, error) {
	if plan.Adapter != adapterName {
		return proxy.Receipt{}, errors.New("plan does not belong to Mihomo adapter")
	}
	var payload planPayload
	if err := json.Unmarshal(plan.Payload, &payload); err != nil {
		return proxy.Receipt{}, fmt.Errorf("decode Mihomo plan: %w", err)
	}
	previous, existed, err := readOptionalFile(a.config.ProviderFile)
	if err != nil {
		return proxy.Receipt{}, err
	}
	actualHash := missingFileHash
	if existed {
		actualHash = contentHash(previous)
	}
	if actualHash != payload.ExpectedHash {
		return proxy.Receipt{}, errors.New("Mihomo provider changed after planning")
	}
	changed := !bytes.Equal(previous, payload.Content) || !existed
	if changed {
		if err := fsutil.WriteFileAtomic(a.config.ProviderFile, payload.Content, 0o640); err != nil {
			return proxy.Receipt{}, fmt.Errorf("write Mihomo provider: %w", err)
		}
		if err := a.reload(ctx); err != nil {
			_ = restoreOptionalFile(a.config.ProviderFile, previous, existed)
			return proxy.Receipt{}, err
		}
	}
	receiptData, err := json.Marshal(receiptPayload{
		PreviousExists: existed, Previous: previous, AppliedHash: contentHash(payload.Content), Rules: payload.Rules,
	})
	if err != nil {
		return proxy.Receipt{}, err
	}
	return proxy.Receipt{ID: plan.ID, Adapter: adapterName, Changed: changed, AppliedAt: time.Now().UTC(), Payload: receiptData}, nil
}

// Verify 轮询活动规则列表，确认每条受管规则已经以 DIRECT 生效。
func (a *Adapter) Verify(ctx context.Context, _ proxy.DirectPolicy, receipt proxy.Receipt) error {
	var payload receiptPayload
	if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
		return err
	}
	deadline := time.Now().Add(a.config.Timeout.Duration())
	for {
		if err := a.verifyOnce(ctx, payload.Rules); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return err
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Rollback 仅在文件仍是本次应用版本时恢复原内容，并重新加载配置。
func (a *Adapter) Rollback(ctx context.Context, receipt proxy.Receipt) error {
	if !receipt.Changed {
		return nil
	}
	var payload receiptPayload
	if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
		return err
	}
	current, exists, err := readOptionalFile(a.config.ProviderFile)
	if err != nil {
		return err
	}
	if !exists || contentHash(current) != payload.AppliedHash {
		return errors.New("Mihomo provider changed after apply; refusing to overwrite it during rollback")
	}
	if err := restoreOptionalFile(a.config.ProviderFile, payload.Previous, payload.PreviousExists); err != nil {
		return err
	}
	return a.reload(ctx)
}

func (a *Adapter) reload(ctx context.Context) error {
	if a.config.ReloadConfig == "" {
		return nil
	}
	body, err := json.Marshal(map[string]string{"path": a.config.ReloadConfig})
	if err != nil {
		return err
	}
	_, status, err := a.request(ctx, http.MethodPut, "/configs?force=true", body)
	if err != nil {
		return fmt.Errorf("reload Mihomo: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("Mihomo reload returned %d", status)
	}
	return nil
}

func (a *Adapter) verifyOnce(ctx context.Context, expected []string) error {
	body, status, err := a.request(ctx, http.MethodGet, "/rules", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("Mihomo rules endpoint returned %d", status)
	}
	var response struct {
		Rules []struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
			Proxy   string `json:"proxy"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decode Mihomo rules: %w", err)
	}
	active := map[string]bool{}
	for _, rule := range response.Rules {
		if strings.EqualFold(rule.Proxy, "DIRECT") {
			active[rule.Type+","+rule.Payload] = true
		}
	}
	for _, rule := range expected {
		parts := strings.Split(rule, ",")
		if len(parts) < 3 || !active[parts[0]+","+parts[1]] {
			return fmt.Errorf("managed rule %q is not active as DIRECT", rule)
		}
	}
	return nil
}

func (a *Adapter) request(ctx context.Context, method, endpoint string, body []byte) ([]byte, int, error) {
	target := *a.controller
	path, query, _ := strings.Cut(endpoint, "?")
	target.Path = strings.TrimRight(target.Path, "/") + path
	target.RawQuery = query
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	if a.config.Secret != "" {
		request.Header.Set("Authorization", "Bearer "+a.config.Secret)
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := a.client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponse))
	if err != nil {
		return nil, response.StatusCode, err
	}
	return responseBody, response.StatusCode, nil
}

func rulesForPolicy(policy proxy.DirectPolicy) []string {
	rules := make([]string, 0, len(policy.IPv4CIDRs)+len(policy.IPv6CIDRs)+len(policy.Domains)+len(policy.Processes))
	for _, process := range policy.Processes {
		rules = append(rules, "PROCESS-NAME,"+process+",DIRECT")
	}
	for _, domain := range policy.Domains {
		ruleType := "DOMAIN"
		if strings.HasPrefix(domain, "+.") || strings.HasPrefix(domain, "*.") {
			ruleType = "DOMAIN-SUFFIX"
			domain = domain[2:]
		}
		rules = append(rules, ruleType+","+domain+",DIRECT")
	}
	for _, prefix := range policy.IPv4CIDRs {
		rules = append(rules, "IP-CIDR,"+prefix+",DIRECT,no-resolve")
	}
	for _, prefix := range policy.IPv6CIDRs {
		rules = append(rules, "IP-CIDR6,"+prefix+",DIRECT,no-resolve")
	}
	return rules
}

func readOptionalFile(path string) ([]byte, bool, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read managed provider: %w", err)
	}
	return content, true, nil
}

func restoreOptionalFile(path string, content []byte, existed bool) error {
	if existed {
		return fsutil.WriteFileAtomic(path, content, 0o640)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove newly created provider: %w", err)
	}
	return nil
}

func contentHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
