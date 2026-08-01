package config

import (
	"path/filepath"
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
