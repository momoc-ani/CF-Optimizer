package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
)

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
	if cfg.Benchmark.Candidates != 42 || cfg.Benchmark.ConnectAttempts != 4 {
		t.Fatalf("defaults were not merged: %#v", cfg.Benchmark)
	}
	if cfg.Schedule.Interval.Duration() != 6*time.Hour {
		t.Fatalf("unexpected interval: %s", cfg.Schedule.Interval)
	}
	if !cfg.Acceleration.Enabled || !cfg.Acceleration.AutoDiscover || !cfg.Acceleration.AutoApply {
		t.Fatalf("域名加速默认值未启用自动发现和自动应用：%#v", cfg.Acceleration)
	}
	if got := cfg.AccelerationDomains(); len(got) != 1 || got[0] != "ani.momoc.top" {
		t.Fatalf("unexpected default acceleration domains: %#v", got)
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

func osWriteFile(path string, data []byte) error {
	return fsutil.WriteFileAtomic(path, data, 0o600)
}
