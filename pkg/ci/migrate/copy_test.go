package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCopyWorkflows tests copying workflow files from .github/workflows to .depot/workflows
func TestCopyWorkflows(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .github/workflows directory with files
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatalf("failed to create workflows directory: %v", err)
	}

	// Create workflow files
	ciFile := filepath.Join(workflowsDir, "ci.yml")
	if err := os.WriteFile(ciFile, []byte("name: CI\non: push\n"), 0644); err != nil {
		t.Fatalf("failed to write ci.yml: %v", err)
	}

	deployFile := filepath.Join(workflowsDir, "deploy.yml")
	if err := os.WriteFile(deployFile, []byte("name: Deploy\non: release\n"), 0644); err != nil {
		t.Fatalf("failed to write deploy.yml: %v", err)
	}

	// Copy
	result, err := CopyGitHubToDepot(tmpDir, []string{"workflows"}, CopyModeError)
	if err != nil {
		t.Fatalf("CopyGitHubToDepot failed: %v", err)
	}

	// Verify files were copied
	if len(result.FilesCopied) != 2 {
		t.Errorf("expected 2 files copied, got %d", len(result.FilesCopied))
	}

	// Verify files exist in destination
	destCI := filepath.Join(tmpDir, ".depot", "workflows", "ci.yml")
	if _, err := os.Stat(destCI); err != nil {
		t.Errorf("ci.yml not found in destination: %v", err)
	}

	destDeploy := filepath.Join(tmpDir, ".depot", "workflows", "deploy.yml")
	if _, err := os.Stat(destDeploy); err != nil {
		t.Errorf("deploy.yml not found in destination: %v", err)
	}

	// Verify content
	content, err := os.ReadFile(destCI)
	if err != nil {
		t.Fatalf("failed to read copied ci.yml: %v", err)
	}
	if string(content) != "name: CI\non: push\n" {
		t.Errorf("ci.yml content mismatch: got %q", string(content))
	}
}

// TestCopyMultipleDirs tests copying multiple subdirectories
func TestCopyMultipleDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .github/workflows
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatalf("failed to create workflows directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte("workflow"), 0644); err != nil {
		t.Fatalf("failed to write workflow file: %v", err)
	}

	// Create .github/actions
	actionsDir := filepath.Join(tmpDir, ".github", "actions")
	if err := os.MkdirAll(actionsDir, 0755); err != nil {
		t.Fatalf("failed to create actions directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(actionsDir, "setup.yml"), []byte("action"), 0644); err != nil {
		t.Fatalf("failed to write action file: %v", err)
	}

	// Copy both
	result, err := CopyGitHubToDepot(tmpDir, []string{"workflows", "actions"}, CopyModeError)
	if err != nil {
		t.Fatalf("CopyGitHubToDepot failed: %v", err)
	}

	// Verify both directories were copied
	if len(result.FilesCopied) != 2 {
		t.Errorf("expected 2 files copied, got %d", len(result.FilesCopied))
	}

	// Verify structure
	if _, err := os.Stat(filepath.Join(tmpDir, ".depot", "workflows", "ci.yml")); err != nil {
		t.Errorf("workflows/ci.yml not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".depot", "actions", "setup.yml")); err != nil {
		t.Errorf("actions/setup.yml not found: %v", err)
	}
}

// TestCopyMissingGitHub tests error when .github directory doesn't exist
func TestCopyMissingGitHub(t *testing.T) {
	tmpDir := t.TempDir()

	// Don't create .github directory
	result, err := CopyGitHubToDepot(tmpDir, []string{"workflows"}, CopyModeError)

	if err == nil {
		t.Fatal("expected error for missing .github directory, got nil")
	}

	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}

	// Verify error message mentions ".github"
	if errMsg := err.Error(); len(errMsg) == 0 || errMsg[0:0] == "" {
		// Just check that error exists and is non-empty
		if len(errMsg) == 0 {
			t.Error("error message is empty")
		}
	}
	// Check that ".github" is mentioned in error
	if errMsg := err.Error(); !strings.Contains(errMsg, ".github") {
		t.Errorf("error message should mention '.github', got: %s", errMsg)
	}
}

// TestCopyExistingDepotError tests error when .depot exists and mode is CopyModeError
func TestCopyExistingDepotError(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .github/workflows
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatalf("failed to create workflows directory: %v", err)
	}

	// Create .depot directory (already exists)
	depotDir := filepath.Join(tmpDir, ".depot")
	if err := os.MkdirAll(depotDir, 0755); err != nil {
		t.Fatalf("failed to create .depot directory: %v", err)
	}

	// Try to copy with CopyModeError
	result, err := CopyGitHubToDepot(tmpDir, []string{"workflows"}, CopyModeError)

	if err == nil {
		t.Fatal("expected error when .depot exists with CopyModeError, got nil")
	}

	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

// TestCopyExistingDepotOverwrite tests overwriting existing .depot with CopyModeOverwrite
func TestCopyExistingDepotOverwrite(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .github/workflows
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatalf("failed to create workflows directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "new.yml"), []byte("new content"), 0644); err != nil {
		t.Fatalf("failed to write new.yml: %v", err)
	}

	// Create existing .depot with old file
	depotDir := filepath.Join(tmpDir, ".depot", "workflows")
	if err := os.MkdirAll(depotDir, 0755); err != nil {
		t.Fatalf("failed to create .depot directory: %v", err)
	}
	oldFile := filepath.Join(depotDir, "old.yml")
	if err := os.WriteFile(oldFile, []byte("old content"), 0644); err != nil {
		t.Fatalf("failed to write old.yml: %v", err)
	}

	// Copy with CopyModeOverwrite
	result, err := CopyGitHubToDepot(tmpDir, []string{"workflows"}, CopyModeOverwrite)
	if err != nil {
		t.Fatalf("CopyGitHubToDepot failed: %v", err)
	}

	// Verify new file was copied
	newFilePath := filepath.Join(tmpDir, ".depot", "workflows", "new.yml")
	if _, err := os.Stat(newFilePath); err != nil {
		t.Errorf("new.yml not found: %v", err)
	}

	// Verify old file still exists (we don't delete, just overwrite)
	if _, err := os.Stat(oldFile); err != nil {
		t.Errorf("old.yml should still exist: %v", err)
	}

	if len(result.FilesCopied) != 1 {
		t.Errorf("expected 1 file copied, got %d", len(result.FilesCopied))
	}
}

// TestCopySkipSymlinks tests that symlinks are skipped with a warning
func TestCopySkipSymlinks(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .github/workflows
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatalf("failed to create workflows directory: %v", err)
	}

	// Create a regular file
	regularFile := filepath.Join(workflowsDir, "regular.yml")
	if err := os.WriteFile(regularFile, []byte("regular"), 0644); err != nil {
		t.Fatalf("failed to write regular.yml: %v", err)
	}

	// Create a symlink
	symlinkFile := filepath.Join(workflowsDir, "link.yml")
	targetFile := filepath.Join(workflowsDir, "target.yml")
	if err := os.WriteFile(targetFile, []byte("target"), 0644); err != nil {
		t.Fatalf("failed to write target.yml: %v", err)
	}
	if err := os.Symlink(targetFile, symlinkFile); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	// Copy
	result, err := CopyGitHubToDepot(tmpDir, []string{"workflows"}, CopyModeError)
	if err != nil {
		t.Fatalf("CopyGitHubToDepot failed: %v", err)
	}

	// Verify regular file was copied
	if len(result.FilesCopied) != 2 { // regular.yml and target.yml
		t.Errorf("expected 2 files copied (regular + target), got %d", len(result.FilesCopied))
	}

	// Verify symlink was skipped
	if len(result.Warnings) == 0 {
		t.Error("expected warning about skipped symlink, got none")
	}

	// Verify warning mentions symlink
	hasSymlinkWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "symlink") {
			hasSymlinkWarning = true
			break
		}
	}
	if !hasSymlinkWarning {
		t.Errorf("expected symlink warning, got: %v", result.Warnings)
	}

	// Verify symlink was not copied
	symlinkDest := filepath.Join(tmpDir, ".depot", "workflows", "link.yml")
	if _, err := os.Stat(symlinkDest); err == nil {
		t.Error("symlink should not have been copied")
	}
}

// TestCopyEmptyDir tests copying an empty directory
func TestCopyEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create empty .github/workflows
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatalf("failed to create workflows directory: %v", err)
	}

	// Copy
	result, err := CopyGitHubToDepot(tmpDir, []string{"workflows"}, CopyModeError)
	if err != nil {
		t.Fatalf("CopyGitHubToDepot failed: %v", err)
	}

	// Verify no files were copied
	if len(result.FilesCopied) != 0 {
		t.Errorf("expected 0 files copied, got %d", len(result.FilesCopied))
	}

	// Verify no warnings
	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings, got %v", result.Warnings)
	}
}

// TestCopyMissingSubDir tests requesting a subdirectory that doesn't exist
func TestCopyMissingSubDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .github/workflows
	workflowsDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatalf("failed to create workflows directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte("workflow"), 0644); err != nil {
		t.Fatalf("failed to write ci.yml: %v", err)
	}

	// Don't create .github/actions

	// Copy both workflows and actions
	result, err := CopyGitHubToDepot(tmpDir, []string{"workflows", "actions"}, CopyModeError)
	if err != nil {
		t.Fatalf("CopyGitHubToDepot failed: %v", err)
	}

	// Verify workflows was copied
	if len(result.FilesCopied) != 1 {
		t.Errorf("expected 1 file copied, got %d", len(result.FilesCopied))
	}

	// Verify warning about missing actions
	if len(result.Warnings) == 0 {
		t.Error("expected warning about missing actions directory, got none")
	}

	hasActionsWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "actions") {
			hasActionsWarning = true
			break
		}
	}
	if !hasActionsWarning {
		t.Errorf("expected warning about 'actions', got: %v", result.Warnings)
	}

	// Verify workflows was copied
	if _, err := os.Stat(filepath.Join(tmpDir, ".depot", "workflows", "ci.yml")); err != nil {
		t.Errorf("workflows/ci.yml not found: %v", err)
	}
}

// TestCopyWorkflowSiblings verifies that non-YAML sibling files under
// .github/workflows/ are mirrored into .depot/workflows/ while YAML workflows and
// symlinks are skipped.
func TestCopyWorkflowSiblings(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, ".github", "workflows")
	if err := os.MkdirAll(filepath.Join(srcDir, "scripts"), 0755); err != nil {
		t.Fatalf("failed to create source dir: %v", err)
	}

	// YAML workflow — should NOT be copied by this helper.
	if err := os.WriteFile(filepath.Join(srcDir, "ci.yml"), []byte("name: CI\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Sibling script at the top level and in a subdirectory — should be copied.
	if err := os.WriteFile(filepath.Join(srcDir, "helper.sh"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "scripts", "deploy.sh"), []byte("#!/bin/sh\n"), 0644); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(tmpDir, ".depot", "workflows")
	copied, err := CopyWorkflowSiblings(srcDir, destDir)
	if err != nil {
		t.Fatalf("CopyWorkflowSiblings failed: %v", err)
	}

	if len(copied) != 2 {
		t.Errorf("expected 2 sibling files copied, got %d: %v", len(copied), copied)
	}

	// YAML must not be mirrored by this helper.
	if _, err := os.Stat(filepath.Join(destDir, "ci.yml")); !os.IsNotExist(err) {
		t.Errorf("expected ci.yml NOT to be copied, stat err: %v", err)
	}

	// Siblings must exist with content and layout preserved.
	helper := filepath.Join(destDir, "helper.sh")
	if data, err := os.ReadFile(helper); err != nil {
		t.Errorf("expected helper.sh copied: %v", err)
	} else if !strings.Contains(string(data), "echo hi") {
		t.Errorf("helper.sh content not preserved: %q", data)
	}
	// Executable bit preserved.
	if info, err := os.Stat(helper); err != nil {
		t.Errorf("stat helper.sh: %v", err)
	} else if info.Mode().Perm()&0100 == 0 {
		t.Errorf("expected helper.sh to stay executable, got %v", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(destDir, "scripts", "deploy.sh")); err != nil {
		t.Errorf("expected scripts/deploy.sh copied preserving layout: %v", err)
	}
}

// TestCopyWorkflowSiblings_OverwritePreservesMode verifies that re-copying over an
// existing destination file resets its mode to match the source, since OpenFile only
// applies the mode when creating a new file.
func TestCopyWorkflowSiblings_OverwritePreservesMode(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, ".github", "workflows")
	destDir := filepath.Join(tmpDir, ".depot", "workflows")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Executable source, but a pre-existing non-executable destination.
	if err := os.WriteFile(filepath.Join(srcDir, "helper.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "helper.sh"), []byte("stale\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := CopyWorkflowSiblings(srcDir, destDir); err != nil {
		t.Fatalf("CopyWorkflowSiblings failed: %v", err)
	}

	info, err := os.Stat(filepath.Join(destDir, "helper.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("expected overwritten sibling mode 0755, got %v", info.Mode().Perm())
	}
}
