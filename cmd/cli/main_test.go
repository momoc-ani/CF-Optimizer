package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInitAndValidateCommands(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	dataDirectory := filepath.Join(directory, "data")
	var output bytes.Buffer
	var errors bytes.Buffer
	if err := run([]string{"--config", configPath, "--data-dir", dataDirectory, "init"}, &output, &errors); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"--config", configPath, "config", "validate"}, &output, &errors); err != nil {
		t.Fatal(err)
	}
	if output.String() != "{\n  \"valid\": true\n}\n" {
		t.Fatalf("unexpected validation output: %q", output.String())
	}
}
