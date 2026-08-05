package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
	"gopkg.in/yaml.v3"
)

const (
	SchemaVersion              = 1
	mihomoUnixControllerScheme = "unix"
)

// Duration 为配置中的可读时间长度提供 YAML 与 JSON 编解码。
type Duration time.Duration

// Duration 返回标准库的 time.Duration 值。
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String 返回 Go 时间长度格式。
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalText 将时间长度编码为可读字符串。
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText 解析 Go 时间长度字符串。
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON 将时间长度编码为 JSON 字符串，避免纳秒整数歧义。
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON 从 JSON 字符串解析时间长度。
func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return d.UnmarshalText([]byte(value))
}

// Config 汇总版本化的服务端配置。
type Config struct {
	Version      int                `yaml:"version" json:"version"`
	DataDir      string             `yaml:"data_dir,omitempty" json:"data_dir"`
	Schedule     ScheduleConfig     `yaml:"schedule" json:"schedule"`
	Ranges       RangesConfig       `yaml:"ranges" json:"ranges"`
	Benchmark    BenchmarkConfig    `yaml:"benchmark" json:"benchmark"`
	Network      NetworkConfig      `yaml:"network" json:"network"`
	Proxy        ProxyConfig        `yaml:"proxy" json:"proxy"`
	Acceleration AccelerationConfig `yaml:"acceleration" json:"acceleration"`
	Hosts        HostsConfig        `yaml:"hosts" json:"hosts"`
	IPC          IPCConfig          `yaml:"ipc" json:"ipc"`
	History      HistoryConfig      `yaml:"history" json:"history"`
}

// AccelerationConfig 定义精确域名加速、自动发现和自动应用边界。
type AccelerationConfig struct {
	Enabled                  bool     `yaml:"enabled" json:"enabled"`
	ManualDomains            []string `yaml:"manual_domains" json:"manual_domains"`
	ExcludedDomains          []string `yaml:"excluded_domains" json:"excluded_domains"`
	AutoDiscover             bool     `yaml:"auto_discover" json:"auto_discover"`
	AutoApply                bool     `yaml:"auto_apply" json:"auto_apply"`
	DiscoveryInterval        Duration `yaml:"discovery_interval" json:"discovery_interval"`
	MaxDiscoveredDomains     int      `yaml:"max_discovered_domains" json:"max_discovered_domains"`
	ApplyVerificationTimeout Duration `yaml:"apply_verification_timeout" json:"apply_verification_timeout"`
	ApplyAttemptTimeout      Duration `yaml:"apply_attempt_timeout" json:"apply_attempt_timeout"`
	ApplyRetryInterval       Duration `yaml:"apply_retry_interval" json:"apply_retry_interval"`
	ApplyMaxAttempts         int      `yaml:"apply_max_attempts" json:"apply_max_attempts"`
}

// ScheduleConfig 定义周期任务与网络变化检测行为。
type ScheduleConfig struct {
	Enabled            bool     `yaml:"enabled" json:"enabled"`
	Interval           Duration `yaml:"interval" json:"interval"`
	RunOnNetworkChange bool     `yaml:"run_on_network_change" json:"run_on_network_change"`
	NetworkPoll        Duration `yaml:"network_poll" json:"network_poll"`
}

// RangesConfig 定义官方网段来源、刷新和安全校验阈值。
type RangesConfig struct {
	Source           string   `yaml:"source" json:"source"`
	APIURL           string   `yaml:"api_url" json:"api_url"`
	IPv4URL          string   `yaml:"ipv4_url" json:"ipv4_url"`
	IPv6URL          string   `yaml:"ipv6_url" json:"ipv6_url"`
	RefreshInterval  Duration `yaml:"refresh_interval" json:"refresh_interval"`
	StaleAfter       Duration `yaml:"stale_after" json:"stale_after"`
	MaxChangePercent float64  `yaml:"max_change_percent" json:"max_change_percent"`
	RequestTimeout   Duration `yaml:"request_timeout" json:"request_timeout"`
	Include          []string `yaml:"include" json:"include"`
	Exclude          []string `yaml:"exclude" json:"exclude"`
}

// BenchmarkConfig 定义候选数量、测速边界和切换策略。
type BenchmarkConfig struct {
	IPv4                bool     `yaml:"ipv4" json:"ipv4"`
	IPv6                bool     `yaml:"ipv6" json:"ipv6"`
	Candidates          int      `yaml:"candidates" json:"candidates"`
	ConnectAttempts     int      `yaml:"connect_attempts" json:"connect_attempts"`
	Concurrency         int      `yaml:"concurrency" json:"concurrency"`
	ConnectTimeout      Duration `yaml:"connect_timeout" json:"connect_timeout"`
	LatencyLimit        Duration `yaml:"latency_limit" json:"latency_limit"`
	LossLimit           float64  `yaml:"loss_limit" json:"loss_limit"`
	DownloadTop         int      `yaml:"download_top" json:"download_top"`
	DownloadConcurrency int      `yaml:"download_concurrency" json:"download_concurrency"`
	DownloadURL         string   `yaml:"download_url" json:"download_url"`
	TLSServerName       string   `yaml:"tls_server_name" json:"tls_server_name"`
	TLSTimeout          Duration `yaml:"tls_timeout" json:"tls_timeout"`
	DownloadDuration    Duration `yaml:"download_duration" json:"download_duration"`
	DownloadMaxBytes    int64    `yaml:"download_max_bytes" json:"download_max_bytes"`
	SwitchImprovement   float64  `yaml:"switch_improvement" json:"switch_improvement"`
	MinimumHold         Duration `yaml:"minimum_hold" json:"minimum_hold"`
	FailureThreshold    int      `yaml:"failure_threshold" json:"failure_threshold"`
	FailureCooldown     Duration `yaml:"failure_cooldown" json:"failure_cooldown"`
	DailySeed           string   `yaml:"daily_seed" json:"daily_seed"`
}

// NetworkConfig 定义物理接口、网关和路由修改开关。
type NetworkConfig struct {
	Interface      string   `yaml:"interface" json:"interface"`
	GatewayIPv4    string   `yaml:"gateway_ipv4" json:"gateway_ipv4"`
	GatewayIPv6    string   `yaml:"gateway_ipv6" json:"gateway_ipv6"`
	ManageRoutes   bool     `yaml:"manage_routes" json:"manage_routes"`
	CommandTimeout Duration `yaml:"command_timeout" json:"command_timeout"`
}

// ProxyConfig 定义代理内核探测和适配器配置。
type ProxyConfig struct {
	AutoDetect bool                `yaml:"auto_detect" json:"auto_detect"`
	Generic    GenericProxyConfig  `yaml:"generic" json:"generic"`
	Mihomo     MihomoConfig        `yaml:"mihomo" json:"mihomo"`
	SingBox    ManagedProxyConfig  `yaml:"sing_box" json:"sing_box"`
	Xray       ManagedProxyConfig  `yaml:"xray" json:"xray"`
	External   ExternalProxyConfig `yaml:"external" json:"external"`
}

// GenericProxyConfig 控制只维护系统路由的通用适配器。
type GenericProxyConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// MihomoConfig 定义受管理的 Mihomo 控制端与规则提供文件。
type MihomoConfig struct {
	Enabled      bool     `yaml:"enabled" json:"enabled"`
	Controller   string   `yaml:"controller" json:"controller"`
	Secret       string   `yaml:"secret" json:"-"`
	ProviderFile string   `yaml:"provider_file" json:"provider_file"`
	ReloadConfig string   `yaml:"reload_config" json:"reload_config"`
	Timeout      Duration `yaml:"timeout" json:"timeout"`
}

// ManagedProxyConfig 定义 sing-box 与 Xray 的受管配置片段及验证/重载命令。
type ManagedProxyConfig struct {
	Enabled        bool     `yaml:"enabled" json:"enabled"`
	Executable     string   `yaml:"executable" json:"executable"`
	ManagedFile    string   `yaml:"managed_file" json:"managed_file"`
	DirectOutbound string   `yaml:"direct_outbound" json:"direct_outbound"`
	ValidateArgs   []string `yaml:"validate_args" json:"validate_args"`
	ReloadArgs     []string `yaml:"reload_args" json:"reload_args"`
	Timeout        Duration `yaml:"timeout" json:"timeout"`
}

// ExternalProxyConfig 定义版本化 JSON-RPC 外部适配器进程。
type ExternalProxyConfig struct {
	Enabled    bool     `yaml:"enabled" json:"enabled"`
	Executable string   `yaml:"executable" json:"executable"`
	Args       []string `yaml:"args" json:"args"`
	Timeout    Duration `yaml:"timeout" json:"timeout"`
}

// HostsConfig 定义 Windows 可选的受管 Hosts 区块。
type HostsConfig struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Path    string   `yaml:"path" json:"path"`
	Domains []string `yaml:"domains" json:"domains"`
}

// IPCConfig 定义本地特权服务端点。
type IPCConfig struct {
	Endpoint string `yaml:"endpoint" json:"endpoint"`
}

// HistoryConfig 定义摘要、明细保留期限和运行数量上限。
type HistoryConfig struct {
	SummaryRetention Duration `yaml:"summary_retention" json:"summary_retention"`
	DetailRetention  Duration `yaml:"detail_retention" json:"detail_retention"`
	MaxRuns          int      `yaml:"max_runs" json:"max_runs"`
}

// Default 返回不修改系统路由、不产生下载流量的安全默认配置。
func Default() Config {
	return Config{
		Version:  SchemaVersion,
		Schedule: ScheduleConfig{Enabled: true, Interval: Duration(6 * time.Hour), RunOnNetworkChange: true, NetworkPoll: Duration(30 * time.Second)},
		Ranges: RangesConfig{
			Source: "cloudflare-api", APIURL: "https://api.cloudflare.com/client/v4/ips",
			IPv4URL: "https://www.cloudflare.com/ips-v4", IPv6URL: "https://www.cloudflare.com/ips-v6",
			RefreshInterval: Duration(24 * time.Hour), StaleAfter: Duration(7 * 24 * time.Hour),
			MaxChangePercent: 30, RequestTimeout: Duration(20 * time.Second), Include: []string{}, Exclude: []string{},
		},
		Benchmark: BenchmarkConfig{
			IPv4: true, IPv6: true, Candidates: 1000, ConnectAttempts: 4, Concurrency: 200,
			ConnectTimeout: Duration(1500 * time.Millisecond), LatencyLimit: Duration(300 * time.Millisecond), LossLimit: 0.25,
			DownloadTop: 20, DownloadConcurrency: 5, TLSServerName: "speed.cloudflare.com", TLSTimeout: Duration(5 * time.Second), DownloadDuration: Duration(8 * time.Second),
			DownloadMaxBytes: 32 << 20, SwitchImprovement: 0.15, MinimumHold: Duration(30 * time.Minute),
			FailureThreshold: 3, FailureCooldown: Duration(6 * time.Hour),
		},
		Network: NetworkConfig{CommandTimeout: Duration(10 * time.Second)},
		Proxy: ProxyConfig{
			AutoDetect: true, Generic: GenericProxyConfig{Enabled: true},
			Mihomo:   MihomoConfig{Controller: "http://127.0.0.1:9090", Timeout: Duration(5 * time.Second)},
			SingBox:  ManagedProxyConfig{DirectOutbound: "direct", Timeout: Duration(10 * time.Second)},
			Xray:     ManagedProxyConfig{DirectOutbound: "direct", Timeout: Duration(10 * time.Second)},
			External: ExternalProxyConfig{Timeout: Duration(15 * time.Second)},
		},
		Acceleration: AccelerationConfig{
			Enabled: true, ManualDomains: []string{}, ExcludedDomains: []string{}, AutoDiscover: true, AutoApply: true,
			DiscoveryInterval: Duration(15 * time.Second), MaxDiscoveredDomains: 1000,
			ApplyVerificationTimeout: Duration(20 * time.Second), ApplyAttemptTimeout: Duration(5 * time.Second),
			ApplyRetryInterval: Duration(500 * time.Millisecond), ApplyMaxAttempts: 4,
		},
		Hosts:   HostsConfig{Path: defaultHostsPath()},
		History: HistoryConfig{SummaryRetention: Duration(30 * 24 * time.Hour), DetailRetention: Duration(7 * 24 * time.Hour), MaxRuns: 500},
	}
}

// Load 将 YAML 覆盖合并到默认配置，并执行严格字段和语义校验。
func Load(path, dataDirOverride string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err == nil {
			decoder := yaml.NewDecoder(strings.NewReader(string(data)))
			decoder.KnownFields(true)
			if err := decoder.Decode(&cfg); err != nil {
				return Config{}, fmt.Errorf("decode config: %w", err)
			}
		}
	}
	if dataDirOverride != "" {
		cfg.DataDir = dataDirOverride
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	if cfg.IPC.Endpoint == "" {
		cfg.IPC.Endpoint = DefaultEndpoint(cfg.DataDir)
	}
	cfg.normalizeCollections()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Save 以原子方式保存 YAML 配置。
func Save(path string, cfg Config) error {
	cfg.normalizeCollections()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, 0o600)
}

// normalizeCollections 保证 JSON/YAML 边界使用空数组而不是 null，兼容旧配置迁移和前端表单。
func (c *Config) normalizeCollections() {
	if c.Ranges.Include == nil {
		c.Ranges.Include = []string{}
	}
	if c.Ranges.Exclude == nil {
		c.Ranges.Exclude = []string{}
	}
	if c.Acceleration.ManualDomains == nil {
		c.Acceleration.ManualDomains = []string{}
	}
	if c.Acceleration.ExcludedDomains == nil {
		c.Acceleration.ExcludedDomains = []string{}
	}
	if c.Hosts.Domains == nil {
		c.Hosts.Domains = []string{}
	}
}

// Validate 校验配置边界以及会影响网络安全的组合约束。
func (c Config) Validate() error {
	if c.Version != SchemaVersion {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.Benchmark.Candidates < 1 || c.Benchmark.Candidates > 100000 {
		return errors.New("benchmark.candidates must be between 1 and 100000")
	}
	if c.Benchmark.ConnectAttempts < 1 || c.Benchmark.ConnectAttempts > 20 {
		return errors.New("benchmark.connect_attempts must be between 1 and 20")
	}
	if c.Benchmark.Concurrency < 1 || c.Benchmark.Concurrency > 4096 {
		return errors.New("benchmark.concurrency must be between 1 and 4096")
	}
	if c.Benchmark.DownloadTop < 1 || c.Benchmark.DownloadTop > c.Benchmark.Candidates {
		return errors.New("benchmark.download_top must be between 1 and benchmark.candidates")
	}
	if c.Benchmark.DownloadConcurrency < 1 || c.Benchmark.DownloadConcurrency > 256 {
		return errors.New("benchmark.download_concurrency must be between 1 and 256")
	}
	if c.Benchmark.FailureThreshold < 1 || c.Benchmark.FailureThreshold > 100 {
		return errors.New("benchmark.failure_threshold must be between 1 and 100")
	}
	if strings.TrimSpace(c.Benchmark.TLSServerName) == "" && c.Benchmark.DownloadURL == "" {
		return errors.New("benchmark.tls_server_name is required when download_url is empty")
	}
	if c.Benchmark.DownloadURL != "" && c.Benchmark.DownloadMaxBytes < 1 {
		return errors.New("benchmark.download_max_bytes must be positive when download_url is configured")
	}
	if !c.Benchmark.IPv4 && !c.Benchmark.IPv6 {
		return errors.New("at least one IP family must be enabled")
	}
	if c.Benchmark.LossLimit < 0 || c.Benchmark.LossLimit > 1 {
		return errors.New("benchmark.loss_limit must be between 0 and 1")
	}
	if c.Benchmark.SwitchImprovement < 0 || c.Benchmark.SwitchImprovement > 10 {
		return errors.New("benchmark.switch_improvement must be between 0 and 10")
	}
	if c.Ranges.MaxChangePercent < 0 || c.Ranges.MaxChangePercent > 100 {
		return errors.New("ranges.max_change_percent must be between 0 and 100")
	}
	for name, value := range map[string]Duration{
		"schedule.interval": c.Schedule.Interval, "schedule.network_poll": c.Schedule.NetworkPoll,
		"ranges.refresh_interval": c.Ranges.RefreshInterval,
		"ranges.stale_after":      c.Ranges.StaleAfter, "benchmark.connect_timeout": c.Benchmark.ConnectTimeout,
		"benchmark.latency_limit": c.Benchmark.LatencyLimit, "benchmark.tls_timeout": c.Benchmark.TLSTimeout,
		"benchmark.failure_cooldown":              c.Benchmark.FailureCooldown,
		"network.command_timeout":                 c.Network.CommandTimeout,
		"acceleration.discovery_interval":         c.Acceleration.DiscoveryInterval,
		"acceleration.apply_verification_timeout": c.Acceleration.ApplyVerificationTimeout,
		"acceleration.apply_attempt_timeout":      c.Acceleration.ApplyAttemptTimeout,
		"acceleration.apply_retry_interval":       c.Acceleration.ApplyRetryInterval,
	} {
		if value.Duration() <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if c.Acceleration.ApplyMaxAttempts < 1 || c.Acceleration.ApplyMaxAttempts > 20 {
		return errors.New("acceleration.apply_max_attempts must be between 1 and 20")
	}
	if c.Acceleration.ApplyAttemptTimeout > c.Acceleration.ApplyVerificationTimeout {
		return errors.New("acceleration.apply_attempt_timeout must not exceed apply_verification_timeout")
	}
	for name, raw := range map[string]string{"ranges.api_url": c.Ranges.APIURL, "ranges.ipv4_url": c.Ranges.IPv4URL, "ranges.ipv6_url": c.Ranges.IPv6URL} {
		if err := validateHTTPURL(raw); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if c.Benchmark.DownloadURL != "" {
		if err := validateHTTPSURL(c.Benchmark.DownloadURL); err != nil {
			return fmt.Errorf("benchmark.download_url: %w", err)
		}
	}
	for _, raw := range append(append([]string{}, c.Ranges.Include...), c.Ranges.Exclude...) {
		if _, err := netip.ParsePrefix(raw); err != nil {
			return fmt.Errorf("invalid configured CIDR %q: %w", raw, err)
		}
	}
	if c.Network.ManageRoutes && c.Network.Interface == "" {
		return errors.New("network.interface is required when network.manage_routes is true")
	}
	if err := validateProxyConfig(c.Proxy); err != nil {
		return err
	}
	if c.Acceleration.MaxDiscoveredDomains < 1 || c.Acceleration.MaxDiscoveredDomains > 100000 {
		return errors.New("acceleration.max_discovered_domains must be between 1 and 100000")
	}
	for _, domain := range append(append([]string{}, c.Acceleration.ManualDomains...), c.Acceleration.ExcludedDomains...) {
		if err := validateConfigDomain(domain); err != nil {
			return fmt.Errorf("invalid acceleration domain %q: %w", domain, err)
		}
	}
	if c.Hosts.Enabled {
		if !filepath.IsAbs(c.Hosts.Path) {
			return errors.New("hosts.path must be absolute when Hosts management is enabled")
		}
		for _, domain := range c.Hosts.Domains {
			if err := validateConfigDomain(domain); err != nil {
				return fmt.Errorf("invalid hosts domain %q: %w", domain, err)
			}
		}
	}
	return nil
}

// AccelerationDomains 返回兼容旧 hosts.domains 后的手动精确域名集合。
func (c Config) AccelerationDomains() []string {
	seen := make(map[string]struct{}, len(c.Acceleration.ManualDomains)+len(c.Hosts.Domains))
	excluded := make(map[string]struct{}, len(c.Acceleration.ExcludedDomains))
	for _, domain := range c.Acceleration.ExcludedDomains {
		excluded[strings.ToLower(strings.TrimSpace(domain))] = struct{}{}
	}
	result := make([]string, 0, len(c.Acceleration.ManualDomains)+len(c.Hosts.Domains))
	for _, domain := range append(append([]string{}, c.Acceleration.ManualDomains...), c.Hosts.Domains...) {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			continue
		}
		if _, blocked := excluded[domain]; blocked {
			continue
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	return result
}

// validateProxyConfig 校验各代理适配器启用后的必需字段和本机访问边界。
func validateProxyConfig(proxy ProxyConfig) error {
	if proxy.Mihomo.Enabled {
		if err := validateMihomoController(proxy.Mihomo.Controller); err != nil {
			return err
		}
		if proxy.Mihomo.ProviderFile == "" {
			return errors.New("proxy.mihomo.provider_file is required when Mihomo is enabled")
		}
	}
	for name, managed := range map[string]ManagedProxyConfig{"sing_box": proxy.SingBox, "xray": proxy.Xray} {
		if !managed.Enabled {
			continue
		}
		if managed.ManagedFile == "" || managed.DirectOutbound == "" {
			return fmt.Errorf("proxy.%s.managed_file and direct_outbound are required", name)
		}
		if (len(managed.ValidateArgs) > 0 || len(managed.ReloadArgs) > 0) && managed.Executable == "" {
			return fmt.Errorf("proxy.%s.executable is required when command arguments are configured", name)
		}
	}
	if proxy.External.Enabled && proxy.External.Executable == "" {
		return errors.New("proxy.external.executable is required when the external adapter is enabled")
	}
	if proxy.External.Enabled && !filepath.IsAbs(proxy.External.Executable) {
		return errors.New("proxy.external.executable must be an absolute path")
	}
	return nil
}

// validateMihomoController 仅允许本机回环 HTTP(S) 或绝对 Unix Socket 控制端。
func validateMihomoController(rawController string) error {
	controller, err := url.Parse(rawController)
	if err != nil {
		return errors.New("proxy.mihomo.controller must be a local HTTP(S) or Unix Socket URL")
	}
	if controller.Scheme == mihomoUnixControllerScheme {
		if runtime.GOOS == "windows" {
			return errors.New("proxy.mihomo.controller does not support Unix Socket URLs on Windows")
		}
		if controller.Host != "" || controller.User != nil || controller.RawQuery != "" || controller.Fragment != "" || !path.IsAbs(controller.Path) || controller.Path == "/" {
			return errors.New("proxy.mihomo.controller must use unix:///absolute/socket/path without credentials or query parameters")
		}
		return nil
	}
	if controller.Hostname() == "" || (controller.Scheme != "http" && controller.Scheme != "https") {
		return errors.New("proxy.mihomo.controller must be an absolute HTTP(S) URL or Unix Socket URL")
	}
	host := strings.ToLower(controller.Hostname())
	address := netip.Addr{}
	if parsed, err := netip.ParseAddr(host); err == nil {
		address = parsed
	}
	if host != "localhost" && (!address.IsValid() || !address.IsLoopback()) {
		return errors.New("proxy.mihomo.controller must use a loopback address to protect its secret")
	}
	return nil
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("must be an absolute HTTP(S) URL")
	}
	return nil
}

func validateHTTPSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme != "https" {
		return errors.New("must be an absolute HTTPS URL")
	}
	return nil
}

func validateConfigDomain(domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || len(domain) > 253 || strings.ContainsAny(domain, "\r\n, /\\\x00") {
		return errors.New("domain contains an unsafe character")
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return errors.New("domain label is invalid")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return errors.New("domain label must use ASCII letters, digits, or hyphen")
			}
		}
	}
	return nil
}

func defaultHostsPath() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return filepath.Join(root, "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

// DefaultDataDir 返回系统服务使用的默认状态目录。
func DefaultDataDir() string {
	if runtime.GOOS == "windows" {
		if base := os.Getenv("ProgramData"); base != "" {
			return filepath.Join(base, "CF Optimizer")
		}
	}
	if runtime.GOOS == "darwin" {
		return "/Library/Application Support/CF Optimizer"
	}
	return "/var/lib/cf-optimizer"
}

// UserDataDir 返回普通用户直接运行 CLI 时可写的状态目录。
func UserDataDir() string {
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, "cf-optimizer")
	}
	return ".cf-optimizer"
}

// DefaultEndpoint 返回当前平台的默认本地 IPC 端点。
func DefaultEndpoint(dataDir string) string {
	if runtime.GOOS == "windows" {
		return `\\.\pipe\cf-optimizer-v1`
	}
	return filepath.Join(dataDir, "daemon.sock")
}

// DefaultConfigPath 返回系统服务使用的默认 YAML 配置路径。
func DefaultConfigPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(DefaultDataDir(), "config.yaml")
	}
	if runtime.GOOS == "darwin" {
		return "/Library/Application Support/CF Optimizer/config.yaml"
	}
	return "/etc/cf-optimizer/config.yaml"
}
