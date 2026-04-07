package ci

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

// initBareRemote creates a bare git repo with one commit and returns its path.
func initBareRemote(t *testing.T) string {
	t.Helper()
	bare := t.TempDir()
	run(t, bare, "git", "init", "--bare")

	// Create a temp clone, add a commit, push to bare
	clone := t.TempDir()
	run(t, clone, "git", "clone", bare, ".")
	run(t, clone, "git", "config", "user.email", "test@test.com")
	run(t, clone, "git", "config", "user.name", "test")
	writeFile(t, filepath.Join(clone, "README.md"), "init")
	run(t, clone, "git", "add", ".")
	run(t, clone, "git", "commit", "-m", "initial")
	run(t, clone, "git", "push", "origin", "HEAD")

	return bare
}

// cloneRepo clones the bare remote into a temp dir and returns the clone path.
func cloneRepo(t *testing.T, bare string) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "clone", bare, ".")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "test")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %v failed in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestFindMergeBase_PushedBranch(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	// Create and push a feature branch
	run(t, clone, "git", "checkout", "-b", "feature/test")
	writeFile(t, filepath.Join(clone, "feature.txt"), "feature work")
	run(t, clone, "git", "add", ".")
	run(t, clone, "git", "commit", "-m", "feature commit")
	run(t, clone, "git", "push", "-u", "origin", "feature/test")

	// Add a local-only change (not pushed)
	writeFile(t, filepath.Join(clone, "local.txt"), "local only")
	run(t, clone, "git", "add", ".")
	run(t, clone, "git", "commit", "-m", "local commit")

	baseBranch, mergeBase, err := findMergeBase(clone)
	if err != nil {
		t.Fatalf("findMergeBase failed: %v", err)
	}

	if baseBranch != "origin/feature/test" {
		t.Errorf("expected baseBranch=origin/feature/test, got %q", baseBranch)
	}

	// The merge base should be the SHA of origin/feature/test (the pushed commit)
	expectedSHA := run(t, clone, "git", "rev-parse", "origin/feature/test")
	if mergeBase != expectedSHA {
		t.Errorf("expected mergeBase=%s, got %s", expectedSHA, mergeBase)
	}
}

func TestFindMergeBase_UnpushedBranch(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	// Create a local-only branch (not pushed)
	run(t, clone, "git", "checkout", "-b", "local-only-branch")
	writeFile(t, filepath.Join(clone, "local.txt"), "local work")
	run(t, clone, "git", "add", ".")
	run(t, clone, "git", "commit", "-m", "local commit")

	baseBranch, mergeBase, err := findMergeBase(clone)
	if err != nil {
		t.Fatalf("findMergeBase failed: %v", err)
	}

	// Should fall back to default branch
	defaultBranch := run(t, clone, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	defaultBranch = strings.TrimPrefix(defaultBranch, "refs/remotes/")
	if baseBranch != defaultBranch {
		t.Errorf("expected baseBranch=%s, got %q", defaultBranch, baseBranch)
	}

	// Merge base should be against origin/main (or whatever the default is)
	expectedSHA := run(t, clone, "git", "merge-base", "HEAD", defaultBranch)
	if mergeBase != expectedSHA {
		t.Errorf("expected mergeBase=%s, got %s", expectedSHA, mergeBase)
	}
}

func TestFindMergeBase_OnDefaultBranch(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	// Discover the actual default branch name (may be main or master)
	defaultRef := run(t, clone, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	defaultBranch := strings.TrimPrefix(defaultRef, "refs/remotes/")

	// Add a local commit on the default branch
	run(t, clone, "git", "commit", "--allow-empty", "-m", "local commit on default branch")

	baseBranch, mergeBase, err := findMergeBase(clone)
	if err != nil {
		t.Fatalf("findMergeBase failed: %v", err)
	}

	if baseBranch != defaultBranch {
		t.Errorf("expected baseBranch=%s, got %s", defaultBranch, baseBranch)
	}

	// Merge base should be origin/<default> SHA, not HEAD (since we added a local commit)
	originSHA := run(t, clone, "git", "rev-parse", defaultBranch)
	if mergeBase != originSHA {
		t.Errorf("expected mergeBase=%s (%s), got %s", originSHA, defaultBranch, mergeBase)
	}

	// Verify HEAD is different (has the local commit)
	headSHA := run(t, clone, "git", "rev-parse", "HEAD")
	if headSHA == originSHA {
		t.Error("HEAD should differ from remote default after local commit")
	}
}

func TestReadLocalActions(t *testing.T) {
	t.Run("reads action.yml manifests from .depot/actions", func(t *testing.T) {
		dir := t.TempDir()
		actionsDir := filepath.Join(dir, ".depot", "actions")
		setupDir := filepath.Join(actionsDir, "setup-pnpm")
		if err := os.MkdirAll(setupDir, 0755); err != nil {
			t.Fatal(err)
		}
		manifest := "name: Setup pnpm\nruns:\n  using: composite\n  steps:\n    - run: echo ok\n      shell: bash\n"
		writeFile(t, filepath.Join(setupDir, "action.yml"), manifest)

		result := readLocalActions(dir)
		if len(result) != 1 {
			t.Fatalf("expected 1 action, got %d", len(result))
		}
		if result["setup-pnpm"] != manifest {
			t.Errorf("manifest mismatch: got %q", result["setup-pnpm"])
		}
	})

	t.Run("prefers action.yml over action.yaml", func(t *testing.T) {
		dir := t.TempDir()
		actionsDir := filepath.Join(dir, ".depot", "actions")
		setupDir := filepath.Join(actionsDir, "my-action")
		if err := os.MkdirAll(setupDir, 0755); err != nil {
			t.Fatal(err)
		}
		ymlContent := "yml-content"
		yamlContent := "yaml-content"
		writeFile(t, filepath.Join(setupDir, "action.yml"), ymlContent)
		writeFile(t, filepath.Join(setupDir, "action.yaml"), yamlContent)

		result := readLocalActions(dir)
		if result["my-action"] != ymlContent {
			t.Errorf("expected action.yml to take precedence, got %q", result["my-action"])
		}
	})

	t.Run("falls back to action.yaml", func(t *testing.T) {
		dir := t.TempDir()
		actionsDir := filepath.Join(dir, ".depot", "actions")
		setupDir := filepath.Join(actionsDir, "my-action")
		if err := os.MkdirAll(setupDir, 0755); err != nil {
			t.Fatal(err)
		}
		yamlContent := "yaml-fallback"
		writeFile(t, filepath.Join(setupDir, "action.yaml"), yamlContent)

		result := readLocalActions(dir)
		if result["my-action"] != yamlContent {
			t.Errorf("expected action.yaml fallback, got %q", result["my-action"])
		}
	})

	t.Run("skips directories without manifests", func(t *testing.T) {
		dir := t.TempDir()
		actionsDir := filepath.Join(dir, ".depot", "actions")
		emptyDir := filepath.Join(actionsDir, "no-manifest")
		if err := os.MkdirAll(emptyDir, 0755); err != nil {
			t.Fatal(err)
		}
		withDir := filepath.Join(actionsDir, "has-manifest")
		if err := os.MkdirAll(withDir, 0755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(withDir, "action.yml"), "content")

		result := readLocalActions(dir)
		if len(result) != 1 {
			t.Fatalf("expected 1 action, got %d: %v", len(result), result)
		}
		if _, ok := result["no-manifest"]; ok {
			t.Error("should not include directories without manifests")
		}
	})

	t.Run("returns nil when .depot/actions does not exist", func(t *testing.T) {
		dir := t.TempDir()
		result := readLocalActions(dir)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("returns nil when .depot/actions is empty", func(t *testing.T) {
		dir := t.TempDir()
		actionsDir := filepath.Join(dir, ".depot", "actions")
		if err := os.MkdirAll(actionsDir, 0755); err != nil {
			t.Fatal(err)
		}

		result := readLocalActions(dir)
		if result != nil {
			t.Errorf("expected nil for empty directory, got %v", result)
		}
	})

	t.Run("uses the repo root when workflow lives under .depot/workflows", func(t *testing.T) {
		dir := t.TempDir()
		run(t, dir, "git", "init")

		workflowDir := filepath.Join(dir, ".depot", "workflows")
		actionsDir := filepath.Join(dir, ".depot", "actions", "setup-pnpm")
		if err := os.MkdirAll(workflowDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(actionsDir, 0755); err != nil {
			t.Fatal(err)
		}

		writeFile(t, filepath.Join(workflowDir, "ci.yml"), "jobs: {}\n")
		manifest := "name: Setup pnpm\nruns:\n  using: composite\n  steps:\n    - run: echo ok\n      shell: bash\n"
		writeFile(t, filepath.Join(actionsDir, "action.yml"), manifest)

		result := readLocalActionsForWorkflow(workflowDir)
		if len(result) != 1 {
			t.Fatalf("expected 1 action, got %d", len(result))
		}
		if result["setup-pnpm"] != manifest {
			t.Errorf("manifest mismatch: got %q", result["setup-pnpm"])
		}
	})
}

func TestFindMergeBase_DetachedHEAD(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	// Detach HEAD
	headSHA := run(t, clone, "git", "rev-parse", "HEAD")
	run(t, clone, "git", "checkout", headSHA)

	baseBranch, mergeBase, err := findMergeBase(clone)
	if err != nil {
		t.Fatalf("findMergeBase failed: %v", err)
	}

	// Detached HEAD: should fall back to default branch merge base
	if mergeBase != headSHA {
		t.Errorf("expected mergeBase=%s, got %s", headSHA, mergeBase)
	}

	_ = baseBranch
}

func TestValidateWorkspacePatch_emptyMergeBase(t *testing.T) {
	err := validateWorkspacePatch(&patchInfo{mergeBase: "", content: "x"})
	if err == nil {
		t.Fatal("expected error for empty merge base")
	}
	if !strings.Contains(err.Error(), "empty merge base") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateWorkspacePatch_nil(t *testing.T) {
	err := validateWorkspacePatch(nil)
	if err == nil {
		t.Fatal("expected error for nil patch")
	}
	if !strings.Contains(err.Error(), "nil patch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateWorkspacePatch_ok(t *testing.T) {
	if err := validateWorkspacePatch(&patchInfo{mergeBase: "abc", content: "x"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPatchWorkspaceCacheKey_nil(t *testing.T) {
	if got := patchWorkspaceCacheKey(nil); got != "" {
		t.Fatalf("expected empty key for nil patch, got %q", got)
	}
}

func TestPatchWorkspaceCacheKey_format(t *testing.T) {
	mergeBase := "abcdef1234567890abcdef1234567890abcdef12"
	content := "diff --git a/x b/x\n"
	patch := &patchInfo{mergeBase: mergeBase, content: content}
	got := patchWorkspaceCacheKey(patch)

	sum := sha256.Sum256([]byte(content))
	want := fmt.Sprintf("patch/%s/%s", mergeBase[:12], fmt.Sprintf("%x", sum)[:16])
	if got != want {
		t.Fatalf("patchWorkspaceCacheKey() = %q, want %q", got, want)
	}
}

func TestPatchWorkspaceCacheKey_shortMergeBase(t *testing.T) {
	// Avoid panicking if merge base is unexpectedly short (e.g. corrupt git output).
	patch := &patchInfo{mergeBase: "abc", content: "x"}
	got := patchWorkspaceCacheKey(patch)
	sum := sha256.Sum256([]byte("x"))
	want := fmt.Sprintf("patch/%s/%s", "abc", fmt.Sprintf("%x", sum)[:16])
	if got != want {
		t.Fatalf("patchWorkspaceCacheKey() = %q, want %q", got, want)
	}
}

func TestSetRunRequestGitContext_withPatch(t *testing.T) {
	mergeBase := "0123456789ab0123456789ab0123456789abcdef"
	patch := &patchInfo{mergeBase: mergeBase, content: "patch-bytes\n"}
	explicitKey := "patch/explicit/not-from-helper"
	req := &civ1.RunRequest{Repo: "o/r"}
	setRunRequestGitContext(req, patch, "headignored", true, explicitKey)

	if req.GetSha() != mergeBase {
		t.Fatalf("Sha = %q, want merge base %q", req.GetSha(), mergeBase)
	}
	if req.GetWorkspacePatchCacheKey() != explicitKey {
		t.Fatalf("WorkspacePatchCacheKey = %q, want exact passed key %q", req.GetWorkspacePatchCacheKey(), explicitKey)
	}
}

func TestSetRunRequestGitContext_noPatch_usesHead(t *testing.T) {
	req := &civ1.RunRequest{Repo: "o/r"}
	setRunRequestGitContext(req, nil, "deadbeefcafe", true, "")
	if req.GetSha() != "deadbeefcafe" {
		t.Fatalf("Sha = %q, want head SHA", req.GetSha())
	}
	if req.GetWorkspacePatchCacheKey() != "" {
		t.Fatalf("WorkspacePatchCacheKey should be unset, got %q", req.GetWorkspacePatchCacheKey())
	}
}

func TestSetRunRequestGitContext_noPatch_headOK_emptySHA(t *testing.T) {
	req := &civ1.RunRequest{Repo: "o/r"}
	setRunRequestGitContext(req, nil, "", true, "")
	if req.GetSha() != "" {
		t.Fatalf("Sha = %q, want empty when HEAD resolved to empty string", req.GetSha())
	}
	if req.GetWorkspacePatchCacheKey() != "" {
		t.Fatalf("WorkspacePatchCacheKey should be unset, got %q", req.GetWorkspacePatchCacheKey())
	}
}

func TestSetRunRequestGitContext_noPatch_headUnresolved(t *testing.T) {
	req := &civ1.RunRequest{Repo: "o/r"}
	setRunRequestGitContext(req, nil, "", false, "")
	if req.GetSha() != "" {
		t.Fatalf("Sha should be empty, got %q", req.GetSha())
	}
	if req.GetWorkspacePatchCacheKey() != "" {
		t.Fatalf("WorkspacePatchCacheKey should be unset, got %q", req.GetWorkspacePatchCacheKey())
	}
}
