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
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"gopkg.in/yaml.v3"
)

const (
	adapterName          = "mihomo"
	managedFileHeader    = "# Managed by CF Optimizer. Manual changes will be replaced.\n"
	missingFileHash      = "missing"
	maxAPIResponse       = 8 << 20
	unixControllerScheme = "unix"
	unixRequestHost      = "localhost"
)

type planPayload struct {
	Content              []byte   `json:"content"`
	ExpectedHash         string   `json:"expected_hash"`
	ConfigContent        []byte   `json:"config_content,omitempty"`
	ConfigExpectedHash   string   `json:"config_expected_hash,omitempty"`
	MetadataContent      []byte   `json:"metadata_content,omitempty"`
	MetadataExpectedHash string   `json:"metadata_expected_hash,omitempty"`
	Rules                []string `json:"rules"`
}

type receiptPayload struct {
	ProviderFile           string   `json:"provider_file"`
	ConfigFile             string   `json:"config_file,omitempty"`
	MetadataFile           string   `json:"metadata_file,omitempty"`
	PreviousExists         bool     `json:"previous_exists"`
	Previous               []byte   `json:"previous"`
	AppliedHash            string   `json:"applied_hash"`
	ConfigPreviousExists   bool     `json:"config_previous_exists"`
	ConfigPrevious         []byte   `json:"config_previous,omitempty"`
	ConfigAppliedHash      string   `json:"config_applied_hash,omitempty"`
	MetadataPreviousExists bool     `json:"metadata_previous_exists"`
	MetadataPrevious       []byte   `json:"metadata_previous,omitempty"`
	MetadataAppliedHash    string   `json:"metadata_applied_hash,omitempty"`
	Rules                  []string `json:"rules"`
}

// Adapter 管理独立 Mihomo rule-provider 文件，并通过控制 API 重载和验证。
type Adapter struct {
	config                     config.MihomoConfig
	controller                 *url.URL
	endpoint                   string
	client                     *http.Client
	connectionVerifier         func(context.Context, []proxy.DomainMapping) error
	benchmarkDial              cfnetwork.DialContextFunc
	benchmarkInterface         string
	verificationTimeout        time.Duration
	verificationAttemptTimeout time.Duration
	verificationRetryInterval  time.Duration
	verificationMaxAttempts    int
}

// New 创建强制禁用系统代理的 Mihomo 控制 API 客户端。
func New(cfg config.MihomoConfig) (*Adapter, error) {
	controller, endpoint, socketPath, err := parseControllerEndpoint(cfg.Controller)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if socketPath != "" {
		dialer := &net.Dialer{Timeout: cfg.Timeout.Duration()}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, unixControllerScheme, socketPath)
		}
	}
	adapter := &Adapter{
		config: cfg, controller: controller, endpoint: endpoint,
		client:              &http.Client{Transport: transport, Timeout: cfg.Timeout.Duration()},
		verificationTimeout: cfg.Timeout.Duration(), verificationAttemptTimeout: cfg.Timeout.Duration(),
		verificationRetryInterval: mappedConnectionVerificationRetryInterval, verificationMaxAttempts: mappedConnectionVerificationAttempts,
	}
	adapter.connectionVerifier = adapter.verifyMappedConnections
	return adapter, nil
}

// SetConnectionVerificationWindow 设置域名映射的 Mihomo 应用验证窗口，不改变普通控制 API 超时。
func (a *Adapter) SetConnectionVerificationWindow(total, attempt, retry time.Duration, maxAttempts int) {
	if total > 0 {
		a.verificationTimeout = total
	}
	if attempt > 0 && (total <= 0 || attempt <= total) {
		a.verificationAttemptTimeout = attempt
	}
	if retry > 0 {
		a.verificationRetryInterval = retry
	}
	if maxAttempts > 0 {
		a.verificationMaxAttempts = maxAttempts
	}
}

// parseControllerEndpoint 将受支持的控制端转换为 HTTP 请求地址和可选 Unix Socket 路径。
func parseControllerEndpoint(rawController string) (*url.URL, string, string, error) {
	controller, err := url.Parse(rawController)
	if err != nil {
		return nil, "", "", errors.New("invalid Mihomo controller URL")
	}
	if controller.Scheme == unixControllerScheme {
		socketPath, pathErr := unixControllerPath(controller)
		if pathErr != nil {
			return nil, "", "", pathErr
		}
		endpoint := (&url.URL{Scheme: unixControllerScheme, Path: socketPath}).String()
		return &url.URL{Scheme: "http", Host: unixRequestHost}, endpoint, socketPath, nil
	}
	if controller.Host == "" || (controller.Scheme != "http" && controller.Scheme != "https") {
		return nil, "", "", errors.New("invalid Mihomo controller URL")
	}
	return controller, controller.String(), "", nil
}

// unixControllerPath 校验并规范化不携带认证信息的绝对 Unix Socket 路径。
func unixControllerPath(controller *url.URL) (string, error) {
	if controller == nil || controller.Scheme != unixControllerScheme || controller.Host != "" || controller.User != nil || controller.RawQuery != "" || controller.Fragment != "" || !path.IsAbs(controller.Path) || controller.Path == "/" {
		return "", errors.New("invalid Mihomo Unix Socket controller URL")
	}
	return path.Clean(controller.Path), nil
}

// Name 返回稳定的适配器标识。
func (a *Adapter) Name() string { return adapterName }

// Capabilities 返回 Mihomo 受管规则支持的策略类型。
func (a *Adapter) Capabilities() proxy.Capabilities {
	return proxy.Capabilities{Processes: true, IPv4: true, IPv6: true, Domains: true, DomainMappings: a.config.ReloadConfig != "", HotReload: a.config.ReloadConfig != "", Rollback: true}
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
	if strings.TrimSpace(version.Version) == "" {
		return proxy.Detection{Present: false}, errors.New("Mihomo version endpoint returned an empty version")
	}
	return proxy.Detection{
		Present:    true,
		Version:    version.Version,
		Endpoint:   a.endpoint,
		ConfigPath: a.config.ReloadConfig,
		Message:    "控制 API 可访问",
	}, nil
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
	payloadDocument := planPayload{Content: content, ExpectedHash: expectedHash, Rules: rules}
	if len(policy.DomainMappings) > 0 && a.config.ReloadConfig == "" {
		return proxy.Plan{}, errors.New("Mihomo active config path is required for domain mappings")
	}
	if a.config.ReloadConfig != "" {
		configContent, configExists, readErr := readOptionalFile(a.config.ReloadConfig)
		if readErr != nil {
			return proxy.Plan{}, fmt.Errorf("read Mihomo active config: %w", readErr)
		}
		if !configExists {
			return proxy.Plan{}, errors.New("Mihomo active config does not exist")
		}
		metadataPath := managedMetadataPath(a.config.ProviderFile)
		metadataContent, metadataExists, readErr := readOptionalFile(metadataPath)
		if readErr != nil {
			return proxy.Plan{}, fmt.Errorf("read Mihomo managed metadata: %w", readErr)
		}
		patchedConfig, patchedMetadata, patchErr := patchManagedConfig(configContent, metadataContent, metadataExists, policy, rules)
		if patchErr != nil {
			return proxy.Plan{}, patchErr
		}
		payloadDocument.ConfigContent = patchedConfig
		payloadDocument.ConfigExpectedHash = contentHash(configContent)
		payloadDocument.MetadataContent = patchedMetadata
		payloadDocument.MetadataExpectedHash = missingFileHash
		if metadataExists {
			payloadDocument.MetadataExpectedHash = contentHash(metadataContent)
		}
	}
	payload, err := json.Marshal(payloadDocument)
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
	configPrevious, configExisted, metadataPrevious, metadataExisted := []byte(nil), false, []byte(nil), false
	if len(payload.ConfigContent) > 0 {
		configPrevious, configExisted, err = readOptionalFile(a.config.ReloadConfig)
		if err != nil {
			return proxy.Receipt{}, fmt.Errorf("read Mihomo active config: %w", err)
		}
		if !configExisted || contentHash(configPrevious) != payload.ConfigExpectedHash {
			return proxy.Receipt{}, errors.New("Mihomo active config changed after planning")
		}
		metadataPrevious, metadataExisted, err = readOptionalFile(managedMetadataPath(a.config.ProviderFile))
		if err != nil {
			return proxy.Receipt{}, fmt.Errorf("read Mihomo managed metadata: %w", err)
		}
		metadataHash := missingFileHash
		if metadataExisted {
			metadataHash = contentHash(metadataPrevious)
		}
		if metadataHash != payload.MetadataExpectedHash {
			return proxy.Receipt{}, errors.New("Mihomo managed metadata changed after planning")
		}
	}
	providerChanged := !bytes.Equal(previous, payload.Content) || !existed
	configChanged := len(payload.ConfigContent) > 0 && !bytes.Equal(configPrevious, payload.ConfigContent)
	metadataChanged := len(payload.MetadataContent) > 0 && (!metadataExisted || !bytes.Equal(metadataPrevious, payload.MetadataContent))
	changed := providerChanged || configChanged || metadataChanged
	restoreAll := func() {
		if len(payload.ConfigContent) > 0 {
			_ = restoreOptionalFilePreservingMetadata(a.config.ReloadConfig, configPrevious, configExisted)
			_ = restoreOptionalFile(managedMetadataPath(a.config.ProviderFile), metadataPrevious, metadataExisted)
		}
		_ = restoreOptionalFile(a.config.ProviderFile, previous, existed)
	}
	if providerChanged {
		if err := fsutil.WriteFileAtomic(a.config.ProviderFile, payload.Content, 0o640); err != nil {
			return proxy.Receipt{}, fmt.Errorf("write Mihomo provider: %w", err)
		}
	}
	if metadataChanged {
		if err := fsutil.WriteFileAtomic(managedMetadataPath(a.config.ProviderFile), payload.MetadataContent, 0o600); err != nil {
			restoreAll()
			return proxy.Receipt{}, fmt.Errorf("write Mihomo managed metadata: %w", err)
		}
	}
	if configChanged {
		if err := fsutil.WriteFileAtomicPreservingMetadata(a.config.ReloadConfig, payload.ConfigContent, 0o640); err != nil {
			restoreAll()
			return proxy.Receipt{}, fmt.Errorf("write Mihomo active config: %w", err)
		}
	}
	if changed {
		if err := a.reload(ctx); err != nil {
			restoreAll()
			_ = a.reload(ctx)
			return proxy.Receipt{}, err
		}
	}
	receiptData, err := json.Marshal(receiptPayload{
		ProviderFile: a.config.ProviderFile, ConfigFile: a.config.ReloadConfig, MetadataFile: managedMetadataPath(a.config.ProviderFile),
		PreviousExists: existed, Previous: previous, AppliedHash: contentHash(payload.Content),
		ConfigPreviousExists: configExisted, ConfigPrevious: configPrevious, ConfigAppliedHash: optionalAppliedHash(payload.ConfigContent),
		MetadataPreviousExists: metadataExisted, MetadataPrevious: metadataPrevious, MetadataAppliedHash: optionalAppliedHash(payload.MetadataContent),
		Rules: payload.Rules,
	})
	if err != nil {
		return proxy.Receipt{}, err
	}
	return proxy.Receipt{ID: plan.ID, Adapter: adapterName, Changed: changed, AppliedAt: time.Now().UTC(), Payload: receiptData}, nil
}

// Verify 轮询活动规则列表，确认每条受管规则已经以 DIRECT 生效。
func (a *Adapter) Verify(ctx context.Context, policy proxy.DirectPolicy, receipt proxy.Receipt) error {
	var payload receiptPayload
	if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
		return err
	}
	if payload.ConfigAppliedHash != "" {
		content, exists, err := readOptionalFile(payload.ConfigFile)
		if err != nil || !exists || contentHash(content) != payload.ConfigAppliedHash {
			return errors.New("Mihomo active config verification failed")
		}
	}
	if payload.MetadataAppliedHash != "" {
		content, exists, err := readOptionalFile(payload.MetadataFile)
		if err != nil || !exists || contentHash(content) != payload.MetadataAppliedHash {
			return errors.New("Mihomo managed metadata verification failed")
		}
	}
	deadline := time.Now().Add(a.verificationTimeout)
	for {
		if err := a.verifyOnce(ctx, payload.Rules); err == nil {
			if len(policy.DomainMappings) == 0 {
				return nil
			}
			return a.connectionVerifier(ctx, policy.DomainMappings)
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
	providerFile := payload.ProviderFile
	if providerFile == "" {
		providerFile = a.config.ProviderFile
	}
	configFile := payload.ConfigFile
	if configFile == "" {
		configFile = a.config.ReloadConfig
	}
	metadataFile := payload.MetadataFile
	if metadataFile == "" && providerFile != "" {
		metadataFile = managedMetadataPath(providerFile)
	}
	current, exists, err := readOptionalFile(providerFile)
	if err != nil {
		return err
	}
	providerRestored := optionalFileEquals(current, exists, payload.Previous, payload.PreviousExists)
	configCurrent, configExists, metadataCurrent, metadataExists := []byte(nil), false, []byte(nil), false
	configRestored, metadataRestored := true, true
	if payload.ConfigAppliedHash != "" {
		configCurrent, configExists, err = readOptionalFile(configFile)
		if err != nil {
			return err
		}
		configRestored = optionalFileEquals(configCurrent, configExists, payload.ConfigPrevious, payload.ConfigPreviousExists)
	}
	if payload.MetadataAppliedHash != "" {
		metadataCurrent, metadataExists, err = readOptionalFile(metadataFile)
		if err != nil {
			return err
		}
		metadataRestored = optionalFileEquals(metadataCurrent, metadataExists, payload.MetadataPrevious, payload.MetadataPreviousExists)
	}
	if providerRestored && configRestored && metadataRestored {
		return nil
	}
	if !providerRestored && (!exists || contentHash(current) != payload.AppliedHash) {
		return errors.New("Mihomo provider changed after apply; refusing to overwrite it during rollback")
	}
	if !configRestored && (!configExists || contentHash(configCurrent) != payload.ConfigAppliedHash) {
		return errors.New("Mihomo active config changed after apply; refusing to overwrite it during rollback")
	}
	if !metadataRestored && (!metadataExists || contentHash(metadataCurrent) != payload.MetadataAppliedHash) {
		return errors.New("Mihomo managed metadata changed after apply; refusing to overwrite it during rollback")
	}
	if payload.ConfigAppliedHash != "" {
		if err := restoreOptionalFilePreservingMetadata(configFile, payload.ConfigPrevious, payload.ConfigPreviousExists); err != nil {
			return err
		}
	}
	if payload.MetadataAppliedHash != "" {
		if err := restoreOptionalFile(metadataFile, payload.MetadataPrevious, payload.MetadataPreviousExists); err != nil {
			return err
		}
	}
	if err := restoreOptionalFile(providerFile, payload.Previous, payload.PreviousExists); err != nil {
		return err
	}
	return a.reloadConfig(ctx, configFile)
}

// CleanupConflict 在常规收据链中断时，仅撤销仍可由受管标记和元数据证明归属本程序的内容。
func (a *Adapter) CleanupConflict(ctx context.Context, receipts []proxy.Receipt) error {
	if len(receipts) == 0 {
		return nil
	}
	payloads := make([]receiptPayload, 0, len(receipts))
	for _, receipt := range receipts {
		if receipt.Adapter != adapterName || !receipt.Changed {
			continue
		}
		var payload receiptPayload
		if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
			return fmt.Errorf("decode Mihomo cleanup receipt: %w", err)
		}
		payloads = append(payloads, payload)
	}
	if len(payloads) == 0 {
		return nil
	}
	latest := payloads[len(payloads)-1]
	providerFile := latest.ProviderFile
	if providerFile == "" {
		providerFile = a.config.ProviderFile
	}
	currentProvider, providerExists, err := readOptionalFile(providerFile)
	if err != nil {
		return err
	}
	if providerExists && !bytes.HasPrefix(currentProvider, []byte(managedFileHeader)) {
		return errors.New("Mihomo provider has no CF Optimizer ownership marker; refusing cleanup overwrite")
	}

	configFile := latest.ConfigFile
	if configFile == "" {
		configFile = a.config.ReloadConfig
	}
	metadataFile := latest.MetadataFile
	if metadataFile == "" && providerFile != "" {
		metadataFile = managedMetadataPath(providerFile)
	}
	currentConfig, configExists, currentMetadata, metadataExists := []byte(nil), false, []byte(nil), false
	cleanedConfig := []byte(nil)
	if latest.ConfigAppliedHash != "" {
		currentConfig, configExists, err = readOptionalFile(configFile)
		if err != nil {
			return err
		}
		if !configExists {
			return errors.New("Mihomo active config is missing during managed cleanup")
		}
		currentMetadata, metadataExists, err = readOptionalFile(metadataFile)
		if err != nil {
			return err
		}
		if !metadataExists {
			return errors.New("Mihomo managed metadata is missing during conflict cleanup")
		}
		cleanedConfig, err = cleanupManagedConfig(currentConfig, currentMetadata)
		if err != nil {
			return err
		}
	}

	baselineProvider, baselineProviderExists := managedBaseline(payloads, false)
	baselineMetadata, baselineMetadataExists := managedBaseline(payloads, true)
	restoreCurrent := func() {
		if configExists {
			_ = fsutil.WriteFileAtomicPreservingMetadata(configFile, currentConfig, 0o640)
		}
		_ = restoreOptionalFile(providerFile, currentProvider, providerExists)
		if metadataFile != "" {
			_ = restoreOptionalFile(metadataFile, currentMetadata, metadataExists)
		}
	}
	if configExists && !bytes.Equal(currentConfig, cleanedConfig) {
		if err := fsutil.WriteFileAtomicPreservingMetadata(configFile, cleanedConfig, 0o640); err != nil {
			return fmt.Errorf("write cleaned Mihomo active config: %w", err)
		}
	}
	if err := restoreOptionalFile(providerFile, baselineProvider, baselineProviderExists); err != nil {
		restoreCurrent()
		return err
	}
	if metadataFile != "" {
		if err := restoreOptionalFile(metadataFile, baselineMetadata, baselineMetadataExists); err != nil {
			restoreCurrent()
			return err
		}
	}
	if err := a.reloadConfig(ctx, configFile); err != nil {
		if ctx.Err() == nil && controllerUnavailable(err) {
			return nil
		}
		restoreCurrent()
		_ = a.reloadConfig(ctx, configFile)
		return err
	}
	return nil
}

// controllerUnavailable 仅识别控制端连接层错误；HTTP、鉴权和配置错误仍必须中止清理。
func controllerUnavailable(err error) bool {
	var operationErr *net.OpError
	if errors.As(err, &operationErr) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// managedBaseline 返回收据链开始前的非受管内容；截断链中的旧受管版本不应被恢复。
func managedBaseline(payloads []receiptPayload, metadata bool) ([]byte, bool) {
	if len(payloads) == 0 {
		return nil, false
	}
	oldest := payloads[0]
	content, exists := oldest.Previous, oldest.PreviousExists
	if metadata {
		content, exists = oldest.MetadataPrevious, oldest.MetadataPreviousExists
	}
	if !exists {
		return nil, false
	}
	if metadata {
		var document managedMetadata
		if json.Unmarshal(content, &document) == nil && (document.Version == legacyManagedMetadataVersion || document.Version == managedMetadataVersion) {
			return nil, false
		}
		return content, true
	}
	if bytes.HasPrefix(content, []byte(managedFileHeader)) {
		return nil, false
	}
	return content, true
}

func (a *Adapter) reload(ctx context.Context) error {
	return a.reloadConfig(ctx, a.config.ReloadConfig)
}

// reloadConfig 通过控制 API 热加载指定活动配置，并拒绝无路径时伪造成功。
func (a *Adapter) reloadConfig(ctx context.Context, configPath string) error {
	if configPath == "" {
		return nil
	}
	body, err := json.Marshal(map[string]string{"path": configPath})
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
			active[normalizeMihomoRuleType(rule.Type)+","+rule.Payload] = true
		}
	}
	for _, rule := range expected {
		parts := strings.Split(rule, ",")
		if len(parts) < 3 || !active[normalizeMihomoRuleType(parts[0])+","+parts[1]] {
			return fmt.Errorf("managed rule %q is not active as DIRECT", rule)
		}
	}
	return nil
}

// normalizeMihomoRuleType 统一控制 API 与配置文件对同类规则的不同拼写。
func normalizeMihomoRuleType(ruleType string) string {
	normalized := strings.NewReplacer("-", "", "_", "").Replace(strings.ToUpper(strings.TrimSpace(ruleType)))
	if normalized == "IPCIDR6" {
		return "IPCIDR"
	}
	if normalized == "PROCESSNAME" {
		return "PROCESS"
	}
	return normalized
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

// readOptionalFile 区分不存在的受管文件与实际读取失败。
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

// restoreOptionalFile 按收据恢复旧内容，或移除本次新建的受管文件。
func restoreOptionalFile(path string, content []byte, existed bool) error {
	if existed {
		return fsutil.WriteFileAtomic(path, content, 0o640)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove newly created provider: %w", err)
	}
	return nil
}

// restoreOptionalFilePreservingMetadata 恢复第三方活动配置时保留其 owner、group 和权限。
func restoreOptionalFilePreservingMetadata(path string, content []byte, existed bool) error {
	if existed {
		return fsutil.WriteFileAtomicPreservingMetadata(path, content, 0o640)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove newly created third-party config: %w", err)
	}
	return nil
}

func contentHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// optionalAppliedHash 只为实际参与事务的可选文件生成并发校验摘要。
func optionalAppliedHash(content []byte) string {
	if len(content) == 0 {
		return ""
	}
	return contentHash(content)
}

// optionalFileEquals 同时比较可选文件的存在状态和内容。
func optionalFileEquals(current []byte, currentExists bool, previous []byte, previousExists bool) bool {
	if currentExists != previousExists {
		return false
	}
	return !currentExists || bytes.Equal(current, previous)
}
