package project

import (
	"os"
	"path/filepath"
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
}

func TestFindConfigFileUp(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	err := os.MkdirAll(sub, 0755)
	if err != nil {
		t.Fatalf("failed to create temp dirs: %v", err)
	}

	target := filepath.Join(root, "depot.yml")
	err = os.WriteFile(target, []byte("id: test_id\n"), 0644)
	if err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	found, err := FindConfigFileUp(sub)
	if err != nil {
		t.Fatalf("FindConfigFileUp failed: %v", err)
	}
	if found != target {
		t.Errorf("expected %s, got %s", target, found)
	}
}
