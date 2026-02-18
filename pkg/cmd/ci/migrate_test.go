package ci

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/depot/cli/pkg/ci/migrate"
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

func TestRunMigrateYesOverwritesExistingDepotWithoutOverwriteFlag(t *testing.T) {
	tmpDir := t.TempDir()
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatalf("failed to create workflows dir: %v", err)
	}

	workflowPath := filepath.Join(workflowsDir, "ci.yml")
	newContent := "name: CI\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n"
	if err := os.WriteFile(workflowPath, []byte(newContent), 0o644); err != nil {
		t.Fatalf("failed to write workflow: %v", err)
	}

	depotWorkflowsDir := filepath.Join(tmpDir, ".depot", "workflows")
	if err := os.MkdirAll(depotWorkflowsDir, 0o755); err != nil {
		t.Fatalf("failed to create .depot/workflows dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depotWorkflowsDir, "ci.yml"), []byte("old"), 0o644); err != nil {
		t.Fatalf("failed to write old workflow: %v", err)
	}

	err := runMigrate(context.Background(), migrateOptions{
		yes: true,
		dir: tmpDir,
	})
	if err != nil {
		t.Fatalf("expected --yes mode to overwrite existing .depot, got error: %v", err)
	}

	copied, err := os.ReadFile(filepath.Join(depotWorkflowsDir, "ci.yml"))
	if err != nil {
		t.Fatalf("failed to read copied workflow: %v", err)
	}
	if string(copied) != newContent {
		t.Fatalf("expected overwritten workflow content, got %q", string(copied))
	}
}

func TestRunMigrateSkipsInvalidWorkflowFiles(t *testing.T) {
	tmpDir := t.TempDir()
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatalf("failed to create workflows dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(workflowsDir, "valid.yml"), []byte("name: Valid\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n"), 0o644); err != nil {
		t.Fatalf("failed to write valid workflow: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "invalid.yml"), []byte("name: Invalid\non: [push\n"), 0o644); err != nil {
		t.Fatalf("failed to write invalid workflow: %v", err)
	}

	var stdout bytes.Buffer
	err := runMigrate(context.Background(), migrateOptions{
		yes:    true,
		dir:    tmpDir,
		stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("expected migrate to continue with valid files, got error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".depot", "workflows", "valid.yml")); err != nil {
		t.Fatalf("expected valid workflow to be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".depot", "workflows", "invalid.yml")); !os.IsNotExist(err) {
		t.Fatalf("expected invalid workflow to be skipped, got err=%v", err)
	}

	if !strings.Contains(stdout.String(), "skipped invalid workflow") {
		t.Fatalf("expected warning about skipped invalid workflow, got output: %s", stdout.String())
	}
}

func TestCopySelectedWorkflowFilesCopiesOnlySelected(t *testing.T) {
	tmpDir := t.TempDir()
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatalf("failed to create workflows dir: %v", err)
	}

	selectedPath := filepath.Join(workflowsDir, "selected.yml")
	if err := os.WriteFile(selectedPath, []byte("selected"), 0o644); err != nil {
		t.Fatalf("failed to write selected workflow: %v", err)
	}
	unselectedPath := filepath.Join(workflowsDir, "unselected.yml")
	if err := os.WriteFile(unselectedPath, []byte("unselected"), 0o644); err != nil {
		t.Fatalf("failed to write unselected workflow: %v", err)
	}

	copied, err := copySelectedWorkflowFiles(tmpDir, workflowsDir, []*migrate.WorkflowFile{
		{Path: selectedPath},
	})
	if err != nil {
		t.Fatalf("copySelectedWorkflowFiles failed: %v", err)
	}
	if len(copied) != 1 {
		t.Fatalf("expected 1 copied file, got %d", len(copied))
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".depot", "workflows", "selected.yml")); err != nil {
		t.Fatalf("expected selected workflow to be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".depot", "workflows", "unselected.yml")); !os.IsNotExist(err) {
		t.Fatalf("expected unselected workflow to not be copied, got err=%v", err)
	}
}

func TestParseWorkflowDirWithWarningsSkipsInvalidFiles(t *testing.T) {
	workflowsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workflowsDir, "ok.yml"), []byte("name: OK\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n"), 0o644); err != nil {
		t.Fatalf("failed to write valid workflow: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "bad.yml"), []byte("name: Bad\non: [push\n"), 0o644); err != nil {
		t.Fatalf("failed to write invalid workflow: %v", err)
	}

	workflows, warnings, err := parseWorkflowDirWithWarnings(workflowsDir)
	if err != nil {
		t.Fatalf("parseWorkflowDirWithWarnings failed: %v", err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected 1 valid workflow, got %d", len(workflows))
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d (%v)", len(warnings), warnings)
	}
}
