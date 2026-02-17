package ci

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewCmdMigrateFlags(t *testing.T) {
	cmd := NewCmdMigrate()

	flagNames := []string{"yes", "secret", "org", "token", "overwrite"}
	for _, flagName := range flagNames {
		if cmd.Flags().Lookup(flagName) == nil {
			t.Fatalf("expected --%s flag to exist", flagName)
		}
	}
}

func TestRunMigrateYesCopiesFiles(t *testing.T) {
	tmpDir := t.TempDir()
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatalf("failed to create workflows dir: %v", err)
	}

	workflowPath := filepath.Join(workflowsDir, "ci.yml")
	workflowContent := "name: CI\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n"
	if err := os.WriteFile(workflowPath, []byte(workflowContent), 0o644); err != nil {
		t.Fatalf("failed to write workflow: %v", err)
	}

	var stdout bytes.Buffer
	err := runMigrate(context.Background(), migrateOptions{
		yes:    true,
		dir:    tmpDir,
		stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("runMigrate returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".depot", "workflows", "ci.yml")); err != nil {
		t.Fatalf("expected copied workflow file in .depot/workflows: %v", err)
	}
}

func TestRunMigrateMissingGitHubDir(t *testing.T) {
	tmpDir := t.TempDir()

	err := runMigrate(context.Background(), migrateOptions{
		yes: true,
		dir: tmpDir,
	})
	if err == nil {
		t.Fatal("expected error when .github directory is missing")
	}

	if !strings.Contains(err.Error(), ".github") {
		t.Fatalf("expected error to mention .github, got: %v", err)
	}
}

func TestParseSecretAssignments(t *testing.T) {
	assignments, err := parseSecretAssignments([]string{"NPM_TOKEN=abc123", "EMPTY="})
	if err != nil {
		t.Fatalf("parseSecretAssignments returned error: %v", err)
	}

	if assignments["NPM_TOKEN"] != "abc123" {
		t.Fatalf("unexpected value for NPM_TOKEN: %q", assignments["NPM_TOKEN"])
	}
	if assignments["EMPTY"] != "" {
		t.Fatalf("unexpected value for EMPTY: %q", assignments["EMPTY"])
	}
}

func TestRunMigrateWarnsForUnconfiguredDetectedSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatalf("failed to create workflows dir: %v", err)
	}

	workflowPath := filepath.Join(workflowsDir, "ci.yml")
	workflowContent := "name: CI\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ${{ secrets.MISSING_SECRET }}\n"
	if err := os.WriteFile(workflowPath, []byte(workflowContent), 0o644); err != nil {
		t.Fatalf("failed to write workflow: %v", err)
	}

	var stdout bytes.Buffer
	err := runMigrate(context.Background(), migrateOptions{
		yes:    true,
		dir:    tmpDir,
		stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("runMigrate returned error: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "detected secret MISSING_SECRET is not configured") {
		t.Fatalf("expected warning about unconfigured detected secret, got output: %s", output)
	}
}
