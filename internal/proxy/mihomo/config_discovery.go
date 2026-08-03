package mihomo

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"gopkg.in/yaml.v3"
)

// ConfigureDetected 将已验证控制端与自动发现的活动配置文件组合为可管理配置。
func ConfigureDetected(cfg config.MihomoConfig, detection proxy.Detection, dataDir string) (config.MihomoConfig, error) {
	if !detection.Present || detection.Endpoint == "" {
		return cfg, errors.New("Mihomo controller was not detected")
	}
	cfg.Enabled = true
	cfg.Controller = detection.Endpoint
	if cfg.ProviderFile == "" {
		cfg.ProviderFile = filepath.Join(dataDir, "proxy", "mihomo", "cf-optimizer.yaml")
	}
	if cfg.ReloadConfig == "" {
		path, err := discoverActiveConfig(detection.Endpoint)
		if err != nil {
			return cfg, err
		}
		cfg.ReloadConfig = path
	}
	if !filepath.IsAbs(cfg.ProviderFile) || !filepath.IsAbs(cfg.ReloadConfig) {
		return cfg, errors.New("Mihomo managed paths must be absolute")
	}
	return cfg, nil
}

// discoverActiveConfig 选择控制端口匹配且最近更新的 Mihomo 活动配置。
func discoverActiveConfig(controller string) (string, error) {
	type match struct {
		path     string
		modified int64
	}
	var matches []match
	for _, candidate := range activeConfigCandidates() {
		content, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		if !activeConfigMatchesController(content, controller) {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		matches = append(matches, match{path: candidate, modified: info.ModTime().UnixNano()})
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no active Mihomo config matches controller %s", controller)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].modified == matches[j].modified {
			return matches[i].path < matches[j].path
		}
		return matches[i].modified > matches[j].modified
	})
	return matches[0].path, nil
}

// activeConfigMatchesController 同时匹配 Mihomo 的 TCP 与 Unix Socket 控制端字段。
func activeConfigMatchesController(content []byte, controller string) bool {
	var header struct {
		ExternalController     string `yaml:"external-controller"`
		ExternalControllerUnix string `yaml:"external-controller-unix"`
	}
	if yaml.Unmarshal(content, &header) != nil {
		return false
	}
	return sameController(header.ExternalController, controller) || sameController(header.ExternalControllerUnix, controller)
}

// activeConfigCandidates 返回三个桌面平台上受支持客户端的去重配置候选路径。
func activeConfigCandidates() []string {
	patterns := []string{}
	if userConfigDir, err := os.UserConfigDir(); err == nil {
		patterns = append(patterns,
			filepath.Join(userConfigDir, "io.github.clash-verge-rev.clash-verge-rev", "clash-verge.yaml"),
			filepath.Join(userConfigDir, "mihomo", "config.yaml"),
		)
	}
	switch runtime.GOOS {
	case "windows":
		patterns = append(patterns,
			`C:\Users\*\AppData\Roaming\io.github.clash-verge-rev.clash-verge-rev\clash-verge.yaml`,
			`C:\Users\*\AppData\Roaming\io.github.clash-verge.clash-verge\clash-verge.yaml`,
		)
	case "darwin":
		patterns = append(patterns,
			`/Users/*/Library/Application Support/io.github.clash-verge-rev.clash-verge-rev/clash-verge.yaml`,
			`/Users/*/.config/mihomo/config.yaml`,
			`/etc/mihomo/config.yaml`,
		)
	default:
		patterns = append(patterns,
			`/home/*/.local/share/io.github.clash-verge-rev.clash-verge-rev/clash-verge.yaml`,
			`/home/*/.config/mihomo/config.yaml`,
			`/root/.config/mihomo/config.yaml`,
			`/etc/mihomo/config.yaml`,
		)
	}
	seen := map[string]struct{}{}
	var result []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		if len(matches) == 0 && !strings.ContainsAny(pattern, "*?[") {
			matches = []string{pattern}
		}
		for _, candidate := range matches {
			cleaned := filepath.Clean(candidate)
			if _, exists := seen[cleaned]; exists {
				continue
			}
			seen[cleaned] = struct{}{}
			result = append(result, cleaned)
		}
	}
	return result
}

// sameController 比较配置与探测结果中的控制端主机和端口。
func sameController(configured, detected string) bool {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return false
	}
	detectedURL, detectedErr := url.Parse(detected)
	if detectedErr != nil {
		return false
	}
	if detectedURL.Scheme == unixControllerScheme {
		detectedPath, err := unixControllerPath(detectedURL)
		if err != nil {
			return false
		}
		configuredPath := configured
		if strings.HasPrefix(configured, unixControllerScheme+"://") {
			configuredURL, err := url.Parse(configured)
			if err != nil {
				return false
			}
			configuredPath, err = unixControllerPath(configuredURL)
			if err != nil {
				return false
			}
		}
		return path.IsAbs(configuredPath) && path.Clean(configuredPath) == detectedPath
	}
	if !strings.Contains(configured, "://") {
		configured = "http://" + configured
	}
	configuredURL, configuredErr := url.Parse(configured)
	if configuredErr != nil || detectedErr != nil {
		return false
	}
	return strings.EqualFold(configuredURL.Hostname(), detectedURL.Hostname()) && configuredURL.Port() == detectedURL.Port()
}
