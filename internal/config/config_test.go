package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
)

func TestDefaultBenchmarkAndDiscoverySettings(t *testing.T) {
	cfg := Default()
	if cfg.Benchmark.Candidates != 6000 || cfg.Benchmark.DownloadURL != DefaultDownloadURL || cfg.Benchmark.DownloadMaxBytes != DefaultDownloadMaxBytes {
		t.Fatalf("unexpected default benchmark configuration: %#v", cfg.Benchmark)
	}
	if cfg.Acceleration.AutoDiscover {
		t.Fatal("automatic domain discovery must be disabled by default")
	}
	if !cfg.Acceleration.ManualDownloadTest || cfg.Acceleration.ManualDownloadMinMbps != 20 {
		t.Fatalf("unexpected manual domain download defaults: %#v", cfg.Acceleration)
	}
}

func TestLoadMergesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte("version: 1\nbenchmark:\n  candidates: 42\n")
	if err := osWriteFile(path, data); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Benchmark.Candidates != 42 || cfg.Benchmark.ConnectAttempts != 4 || cfg.Benchmark.DownloadConcurrency != 5 || cfg.Benchmark.DownloadURL != DefaultDownloadURL || cfg.Benchmark.DownloadMaxBytes != DefaultDownloadMaxBytes {
		t.Fatalf("defaults were not merged: %#v", cfg.Benchmark)
	}
	if cfg.Schedule.Interval.Duration() != 6*time.Hour {
		t.Fatalf("unexpected interval: %s", cfg.Schedule.Interval)
	}
	if !cfg.Acceleration.Enabled || cfg.Acceleration.AutoDiscover || !cfg.Acceleration.AutoApply {
		t.Fatalf("域名加速默认值未关闭自动发现或未保留自动应用：%#v", cfg.Acceleration)
	}
	if cfg.Acceleration.ApplyVerificationTimeout.Duration() != 20*time.Second ||
		cfg.Acceleration.ApplyAttemptTimeout.Duration() != 5*time.Second ||
		cfg.Acceleration.ApplyRetryInterval.Duration() != 500*time.Millisecond ||
		cfg.Acceleration.ApplyMaxAttempts != 4 {
		t.Fatalf("域名应用验证默认值未合并：%#v", cfg.Acceleration)
	}
	if got := cfg.AccelerationDomains(); len(got) != 0 {
		t.Fatalf("default acceleration domains = %#v, want empty", got)
	}
}

func TestLoadEnablesDefaultDownloadWhenURLIsBlank(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte("version: 1\nbenchmark:\n  download_url: \"\"\n")
	if err := osWriteFile(path, data); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Benchmark.DownloadURL != DefaultDownloadURL || cfg.Benchmark.DownloadMaxBytes != DefaultDownloadMaxBytes {
		t.Fatalf("blank download URL did not enable the default 50 MiB probe: url=%q bytes=%d", cfg.Benchmark.DownloadURL, cfg.Benchmark.DownloadMaxBytes)
	}
}

func TestValidateRejectsInvalidDownloadConcurrency(t *testing.T) {
	for _, value := range []int{0, 257} {
		cfg := Default()
		cfg.Benchmark.DownloadConcurrency = value
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate() error = nil for download concurrency %d", value)
		}
	}
}

func TestValidateRejectsInvalidAccelerationVerificationWindow(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "non-positive apply timeout", mutate: func(cfg *Config) { cfg.Acceleration.ApplyVerificationTimeout = 0 }},
		{name: "attempt exceeds apply window", mutate: func(cfg *Config) {
			cfg.Acceleration.ApplyAttemptTimeout = cfg.Acceleration.ApplyVerificationTimeout + Duration(time.Second)
		}},
		{name: "zero retry interval", mutate: func(cfg *Config) { cfg.Acceleration.ApplyRetryInterval = 0 }},
		{name: "too many attempts", mutate: func(cfg *Config) { cfg.Acceleration.ApplyMaxAttempts = 21 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want invalid acceleration verification configuration")
			}
		})
	}
}

func TestValidateRejectsInvalidManualDomainDownloadThreshold(t *testing.T) {
	for _, value := range []float64{0, -1, 100001} {
		cfg := Default()
		cfg.Acceleration.ManualDownloadMinMbps = value
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate() error = nil for manual domain download threshold %v", value)
		}
	}
}

func TestAccelerationDomainsMergesLegacyAndHonorsExclusions(t *testing.T) {
	cfg := Default()
	cfg.Acceleration.ManualDomains = []string{"ANI.MOMOC.TOP", "manual.example"}
	cfg.Acceleration.ExcludedDomains = []string{"manual.example"}
	cfg.Hosts.Domains = []string{"legacy.example", "ani.momoc.top"}

	got := cfg.AccelerationDomains()
	if len(got) != 2 || got[0] != "ani.momoc.top" || got[1] != "legacy.example" {
		t.Fatalf("AccelerationDomains() = %#v", got)
	}
}

func TestAccelerationDomainsPreservesManualPriorityOrder(t *testing.T) {
	cfg := Default()
	cfg.Acceleration.ManualDomains = []string{"z-priority.example", "a-second.example", "z-priority.example"}
	cfg.Hosts.Domains = []string{"legacy-third.example"}

	got := cfg.AccelerationDomains()
	want := []string{"z-priority.example", "a-second.example", "legacy-third.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AccelerationDomains() = %#v, want %#v", got, want)
	}
}

// TestLoadNormalizesCollectionsForJSON 验证旧配置中的 null 集合不会穿透 IPC 边界。
func TestLoadNormalizesCollectionsForJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte("version: 1\nranges:\n  include: null\n  exclude: null\nacceleration:\n  manual_domains: null\n  excluded_domains: null\nhosts:\n  domains: null\n")
	if err := osWriteFile(path, data); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"include":[]`, `"exclude":[]`, `"manual_domains":[]`, `"excluded_domains":[]`, `"domains":[]`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("normalized config JSON does not contain %s: %s", field, encoded)
		}
	}
}

func TestUnknownConfigFieldRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := osWriteFile(path, []byte("version: 1\nunknown: true\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, dir); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestValidateMihomoUnixController(t *testing.T) {
	cfg := Default()
	cfg.Proxy.Mihomo.Enabled = true
	cfg.Proxy.Mihomo.Controller = "unix:///tmp/verge/verge-mihomo.sock"
	cfg.Proxy.Mihomo.ProviderFile = "/tmp/cf-optimizer-mihomo.yaml"

	err := cfg.Validate()
	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("Validate() error = nil, want Unix controller rejection on Windows")
		}
		return
	}
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsUnsafeMihomoUnixController(t *testing.T) {
	for _, controller := range []string{
		"unix://relative/socket",
		"unix:///tmp/verge/verge-mihomo.sock?token=secret",
	} {
		t.Run(controller, func(t *testing.T) {
			cfg := Default()
			cfg.Proxy.Mihomo.Enabled = true
			cfg.Proxy.Mihomo.Controller = controller
			cfg.Proxy.Mihomo.ProviderFile = "/tmp/cf-optimizer-mihomo.yaml"
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate() error = nil for %q", controller)
			}
		})
	}
}

func osWriteFile(path string, data []byte) error {
	return fsutil.WriteFileAtomic(path, data, 0o600)
}
