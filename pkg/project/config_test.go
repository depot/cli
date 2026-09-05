package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAndReadConfigJSON(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "depot.json")

	cfg := &ProjectConfig{ID: "proj_12345"}
	err := WriteConfig(filename, cfg)
	if err != nil {
		t.Fatalf("WriteConfig failed: %v", err)
	}

	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// Verify JSON is pretty-printed with indent
	if !strings.Contains(string(content), "  \"id\": \"proj_12345\"") {
		t.Errorf("expected indented JSON output, got:\n%s", string(content))
	}

	readCfg, path, err := ReadConfig(dir)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	if path != filename {
		t.Errorf("expected path %s, got %s", filename, path)
	}
	if readCfg.ID != cfg.ID {
		t.Errorf("expected ID %s, got %s", cfg.ID, readCfg.ID)
	}
}

func TestWriteConfigNilGuard(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "depot.json")

	err := WriteConfig(filename, nil)
	if err == nil {
		t.Fatal("expected error when writing nil config, got nil")
	}
	if err.Error() != "config cannot be nil" {
		t.Errorf("expected 'config cannot be nil' error, got %v", err)
	}
}

func TestWriteConfigUnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "depot.txt")

	cfg := &ProjectConfig{ID: "proj_12345"}
	err := WriteConfig(filename, cfg)
	if err == nil {
		t.Fatal("expected error for unsupported extension, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported config file extension") {
		t.Errorf("expected error containing 'unsupported config file extension', got %v", err)
	}
}
