package ci

import (
	"crypto/sha256"
	"encoding/hex"
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
	path := fmt.Sprintf("/patch/%s/%s", mergeBase[:12], fmt.Sprintf("%x", sum)[:16])
	want := "generic" + hex.EncodeToString([]byte(path))
	if got != want {
		t.Fatalf("patchWorkspaceCacheKey() = %q, want %q", got, want)
	}
}

func TestPatchWorkspaceCacheKey_shortMergeBase(t *testing.T) {
	// Avoid panicking if merge base is unexpectedly short (e.g. corrupt git output).
	patch := &patchInfo{mergeBase: "abc", content: "x"}
	got := patchWorkspaceCacheKey(patch)
	sum := sha256.Sum256([]byte("x"))
	path := fmt.Sprintf("/patch/%s/%s", "abc", fmt.Sprintf("%x", sum)[:16])
	want := "generic" + hex.EncodeToString([]byte(path))
	if got != want {
		t.Fatalf("patchWorkspaceCacheKey() = %q, want %q", got, want)
	}
}

func TestSetRunRequestGitContext_withPatch(t *testing.T) {
	mergeBase := "0123456789ab0123456789ab0123456789abcdef"
	patch := &patchInfo{mergeBase: mergeBase, content: "patch-bytes\n"}
	explicitKey := "patch/explicit/not-from-helper"
	req := &civ1.RunRequest{Repo: "o/r"}
	setRunRequestGitContext(req, patch, "headignored", true, explicitKey, "")

	if req.GetSha() != mergeBase {
		t.Fatalf("Sha = %q, want merge base %q", req.GetSha(), mergeBase)
	}
	if req.GetWorkspacePatchCacheKey() != explicitKey {
		t.Fatalf("WorkspacePatchCacheKey = %q, want exact passed key %q", req.GetWorkspacePatchCacheKey(), explicitKey)
	}
}

func TestSetRunRequestGitContext_noPatch_usesHead(t *testing.T) {
	req := &civ1.RunRequest{Repo: "o/r"}
	setRunRequestGitContext(req, nil, "deadbeefcafe", true, "", "")
	if req.GetSha() != "deadbeefcafe" {
		t.Fatalf("Sha = %q, want head SHA", req.GetSha())
	}
	if req.GetWorkspacePatchCacheKey() != "" {
		t.Fatalf("WorkspacePatchCacheKey should be unset, got %q", req.GetWorkspacePatchCacheKey())
	}
}

func TestSetRunRequestGitContext_noPatch_headOK_emptySHA(t *testing.T) {
	req := &civ1.RunRequest{Repo: "o/r"}
	setRunRequestGitContext(req, nil, "", true, "", "")
	if req.GetSha() != "" {
		t.Fatalf("Sha = %q, want empty when HEAD resolved to empty string", req.GetSha())
	}
	if req.GetWorkspacePatchCacheKey() != "" {
		t.Fatalf("WorkspacePatchCacheKey should be unset, got %q", req.GetWorkspacePatchCacheKey())
	}
}

func TestSetRunRequestGitContext_noPatch_headUnresolved(t *testing.T) {
	req := &civ1.RunRequest{Repo: "o/r"}
	setRunRequestGitContext(req, nil, "", false, "", "")
	if req.GetSha() != "" {
		t.Fatalf("Sha should be empty, got %q", req.GetSha())
	}
	if req.GetWorkspacePatchCacheKey() != "" {
		t.Fatalf("WorkspacePatchCacheKey should be unset, got %q", req.GetWorkspacePatchCacheKey())
	}
}

func TestSetRunRequestGitContext_sendsRefWithSha(t *testing.T) {
	patchReq := &civ1.RunRequest{Repo: "o/r"}
	setRunRequestGitContext(patchReq, &patchInfo{mergeBase: "mb"}, "head", true, "key", "refs/heads/feature")
	if patchReq.GetRef() != "refs/heads/feature" {
		t.Fatalf("patch path Ref = %q, want refs/heads/feature", patchReq.GetRef())
	}

	headReq := &civ1.RunRequest{Repo: "o/r"}
	setRunRequestGitContext(headReq, nil, "deadbeefcafe", true, "", "refs/heads/feature")
	if headReq.GetRef() != "refs/heads/feature" {
		t.Fatalf("head path Ref = %q, want refs/heads/feature", headReq.GetRef())
	}
}

func TestSetRunRequestGitContext_omitsRefWithoutSha(t *testing.T) {
	req := &civ1.RunRequest{Repo: "o/r"}
	setRunRequestGitContext(req, nil, "", false, "", "refs/heads/feature")
	if req.Ref != nil {
		t.Fatalf("Ref should be unset without a sha, got %q", req.GetRef())
	}
}

func TestResolveHEADRef(t *testing.T) {
	clone := cloneRepo(t, initBareRemote(t))
	run(t, clone, "git", "checkout", "-b", "feature/test")
	if got := resolveHEADRef(clone); got != "refs/heads/feature/test" {
		t.Fatalf("resolveHEADRef = %q, want refs/heads/feature/test", got)
	}

	run(t, clone, "git", "checkout", "--detach")
	if got := resolveHEADRef(clone); got != "" {
		t.Fatalf("resolveHEADRef on detached HEAD = %q, want empty", got)
	}
}

func TestFindUntrackedDepotFiles_includesNewActions(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	// Create untracked .depot/ files
	depotDir := filepath.Join(clone, ".depot", "actions", "my-action")
	if err := os.MkdirAll(depotDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(depotDir, "action.yml"), "name: test")

	files := findUntrackedDepotFiles(clone)
	if len(files) != 1 {
		t.Fatalf("expected 1 untracked file, got %d: %v", len(files), files)
	}
	if files[0] != ".depot/actions/my-action/action.yml" {
		t.Errorf("expected .depot/actions/my-action/action.yml, got %q", files[0])
	}
}

func TestFindUntrackedDepotFiles_emptyWhenAllTracked(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	files := findUntrackedDepotFiles(clone)
	if len(files) != 0 {
		t.Fatalf("expected no untracked files, got %d: %v", len(files), files)
	}
}

func TestFindUntrackedDepotFiles_excludesNonDepotFiles(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	// Create untracked files both inside and outside .depot/
	depotDir := filepath.Join(clone, ".depot", "actions", "test")
	if err := os.MkdirAll(depotDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(depotDir, "action.yml"), "name: test")
	writeFile(t, filepath.Join(clone, "unrelated.txt"), "not depot")

	files := findUntrackedDepotFiles(clone)
	if len(files) != 1 {
		t.Fatalf("expected 1 untracked file, got %d: %v", len(files), files)
	}
}

func TestDetectPatch_includesUntrackedDepotFiles(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	// Create an untracked .depot/ action
	actionDir := filepath.Join(clone, ".depot", "actions", "local-action")
	if err := os.MkdirAll(actionDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(actionDir, "action.yml"), "name: local\nruns:\n  using: composite\n  steps:\n    - run: echo hi\n      shell: bash")

	patch := detectPatch(clone)
	if patch == nil {
		t.Fatal("expected patch to be generated for untracked .depot/ files")
	}
	if !strings.Contains(patch.content, ".depot/actions/local-action/action.yml") {
		t.Error("patch should contain the new action file")
	}

	// Verify git state is clean after detectPatch
	status := run(t, clone, "git", "status", "--short")
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// All .depot/ files should be back to untracked (??)
		if strings.Contains(line, ".depot/") && !strings.HasPrefix(line, "??") {
			t.Errorf("expected .depot/ files to be untracked after detectPatch, got: %s", line)
		}
	}
}

func TestDetectPatch_noChanges(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	patch := detectPatch(clone)
	if patch != nil {
		t.Fatalf("expected nil patch when no changes, got %+v", patch)
	}
}

func patchStepJobs() map[string]interface{} {
	return map[string]interface{}{
		"build": map[string]interface{}{
			"steps": []interface{}{
				map[string]interface{}{"uses": "actions/checkout@v4"},
				map[string]interface{}{"run": "make test"},
			},
		},
	}
}

func TestInjectPatchStep_downloadWithoutJQ(t *testing.T) {
	for _, downloadFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("downloadFails=%t", downloadFails), func(t *testing.T) {
			repo := cloneRepo(t, initBareRemote(t))
			writeFile(t, filepath.Join(repo, "README.md"), "patched\n")
			patchBytes := run(t, repo, "git", "diff", "--binary") + "\n"
			run(t, repo, "git", "checkout", "--", "README.md")
			patch := &patchInfo{mergeBase: "abc123", content: patchBytes}
			jobs := patchStepJobs()
			sum := sha256.Sum256([]byte(patchBytes))
			path := fmt.Sprintf("patch/abc123/%x", sum[:8])
			injectPatchStep(jobs, "build", patch.mergeBase, path, civ1.Forge_FORGE_GITHUB)
			steps := jobs["build"].(map[string]interface{})["steps"].([]interface{})
			script := steps[1].(map[string]interface{})["run"].(string)
			// Isolate the generated script's temporary file from other test runs.
			script = strings.ReplaceAll(script, "/tmp/local.patch", filepath.Join(t.TempDir(), "local.patch"))
			binDir := t.TempDir()
			for _, name := range []string{"git", "rm"} {
				binary, err := exec.LookPath(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(binary, filepath.Join(binDir, name)); err != nil {
					t.Fatal(err)
				}
			}
			patchFile := filepath.Join(t.TempDir(), "patch")
			writeFile(t, patchFile, patchBytes)
			curl := `#!/bin/bash
set -eu
test "$1" = "-fsSL"
test "$2" = "-H"
test "$3" = "Authorization: Bearer test-token"
test "$4" = "$EXPECTED_URL"
test "$5" = "-o"
if [ "$DOWNLOAD_FAILS" = "true" ]; then exit 22; fi
/bin/cat "$PATCH_FILE" > "$6"
`
			if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte(curl), 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/bash", "-c", script)
			cmd.Dir = repo
			cmd.Env = []string{"PATH=" + binDir, "DEPOT_TOKEN=test-token", "EXPECTED_URL=" + cacheBaseURL + "/" + path, "PATCH_FILE=" + patchFile, fmt.Sprintf("DOWNLOAD_FAILS=%t", downloadFails)}
			output, err := cmd.CombinedOutput()
			if downloadFails && err == nil {
				t.Fatalf("download failure succeeded: %s", output)
			}
			if !downloadFails && err != nil {
				t.Fatalf("patch failed without jq: %v\n%s", err, output)
			}
			content, err := os.ReadFile(filepath.Join(repo, "README.md"))
			if err != nil {
				t.Fatal(err)
			}
			want := "patched\n"
			if downloadFails {
				want = "init"
			}
			if string(content) != want {
				t.Fatalf("README = %q, want %q", content, want)
			}
			if got, want := patchWorkspaceCacheKey(patch), "generic"+hex.EncodeToString([]byte("/"+path)); got != want {
				t.Fatalf("uploaded key = %q, want direct download key %q", got, want)
			}
		})
	}
}

func TestInjectPatchStep_pinsCheckoutToMergeBase(t *testing.T) {
	jobs := patchStepJobs()
	injectPatchStep(jobs, "build", "abc123", "patch/abc123/def", civ1.Forge_FORGE_GITHUB)

	steps := jobs["build"].(map[string]interface{})["steps"].([]interface{})
	if len(steps) != 3 {
		t.Fatalf("got %d steps, want 3", len(steps))
	}
	checkout := steps[0].(map[string]interface{})
	with, ok := checkout["with"].(map[string]interface{})
	if !ok || with["ref"] != "abc123" {
		t.Fatalf("checkout with = %#v, want ref abc123", checkout["with"])
	}
	if steps[1].(map[string]interface{})["name"] != "Apply local patch from Depot Cache" {
		t.Fatalf("step after checkout = %#v, want the patch step", steps[1])
	}
}

// Depot CI checks out from Depot Code only when the checkout step has no inputs,
// so a Depot Code run must not add one.
func TestInjectPatchStep_leavesDepotCodeCheckoutUntouched(t *testing.T) {
	jobs := patchStepJobs()
	injectPatchStep(jobs, "build", "abc123", "patch/abc123/def", civ1.Forge_FORGE_DEPOT_CODE)

	steps := jobs["build"].(map[string]interface{})["steps"].([]interface{})
	if len(steps) != 3 {
		t.Fatalf("got %d steps, want 3", len(steps))
	}
	checkout := steps[0].(map[string]interface{})
	if _, ok := checkout["with"]; ok {
		t.Fatalf("checkout with = %#v, want no inputs", checkout["with"])
	}
	if steps[1].(map[string]interface{})["name"] != "Apply local patch from Depot Cache" {
		t.Fatalf("step after checkout = %#v, want the patch step", steps[1])
	}
}
