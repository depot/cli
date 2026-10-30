package ci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	cachev1 "github.com/depot/cli/pkg/proto/depot/cache/v1"
	"github.com/depot/cli/pkg/proto/depot/cache/v1/cachev1connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"gopkg.in/yaml.v3"
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

func TestInjectPatchStep_appliesDownloadedPatch(t *testing.T) {
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

type reusablePatchRunService struct {
	cachev1connect.UnimplementedCacheServiceHandler
	civ1connect.UnimplementedCIServiceHandler
	cacheKey     string
	cacheContent []byte
	runRequest   *civ1.RunRequest
	presignedURL string
}

func (s *reusablePatchRunService) CreateEntry(_ context.Context, req *connect.Request[cachev1.CreateEntryRequest]) (*connect.Response[cachev1.CreateEntryResponse], error) {
	s.cacheKey = req.Msg.GetKey()
	return connect.NewResponse(&cachev1.CreateEntryResponse{
		EntryId:        "entry",
		UploadPartUrls: []string{s.presignedURL},
	}), nil
}

func (s *reusablePatchRunService) FinalizeEntry(context.Context, *connect.Request[cachev1.FinalizeEntryRequest]) (*connect.Response[cachev1.FinalizeEntryResponse], error) {
	return connect.NewResponse(&cachev1.FinalizeEntryResponse{}), nil
}

func (s *reusablePatchRunService) Run(_ context.Context, req *connect.Request[civ1.RunRequest]) (*connect.Response[civ1.RunResponse], error) {
	s.runRequest = req.Msg
	return connect.NewResponse(&civ1.RunResponse{OrgId: "org", RunId: "run"}), nil
}

func TestRun_reusableJobKeepsPatchKeyWithoutWarning(t *testing.T) {
	service := &reusablePatchRunService{}
	mux := http.NewServeMux()
	cachePath, cacheHandler := cachev1connect.NewCacheServiceHandler(service)
	mux.Handle(cachePath, cacheHandler)
	ciPath, ciHandler := civ1connect.NewCIServiceHandler(service)
	mux.Handle(ciPath, ciHandler)
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		service.cacheContent, _ = io.ReadAll(r.Body)
		w.Header().Set("ETag", `"test-etag"`)
	})
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	service.presignedURL = server.URL + "/upload"
	t.Setenv("DEPOT_API_URL", server.URL)
	t.Setenv("DEPOT_CACHE_HOST", server.URL)

	bare := initBareRemote(t)
	repo := cloneRepo(t, bare)
	workflowPath := filepath.Join(repo, ".depot", "workflows", "ci.yml")
	if err := os.MkdirAll(filepath.Dir(workflowPath), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, workflowPath, `name: ci
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: echo build
  reusable:
    uses: ./.depot/workflows/reusable.yml
`)
	run(t, repo, "git", "add", ".depot/workflows/ci.yml")
	run(t, repo, "git", "commit", "-m", "add workflow")
	run(t, repo, "git", "push", "origin", "HEAD")

	writeFile(t, filepath.Join(repo, "README.md"), "local changes")
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr-")
	if err != nil {
		t.Fatal(err)
	}
	originalStderr := os.Stderr
	os.Stderr = stderrFile
	t.Cleanup(func() {
		os.Stderr = originalStderr
	})

	cmd := NewCmdRun()
	cmd.SetArgs([]string{
		"--workflow", workflowPath,
		"--job", "build",
		"--job", "reusable",
		"--repo", "owner/repo",
		"--forge", "github",
		"--token", "test-token",
		"--org", "org",
	})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := stderrFile.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	stderr, err := io.ReadAll(stderrFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stderr), "calls a reusable workflow") {
		t.Fatalf("reusable workflow warning = %q, want no warning", stderr)
	}
	if service.runRequest == nil {
		t.Fatal("CI run request was not sent")
	}
	if service.runRequest.GetWorkspacePatchCacheKey() == "" || service.runRequest.GetWorkspacePatchCacheKey() != service.cacheKey {
		t.Fatalf("run patch key = %q, uploaded cache key = %q", service.runRequest.GetWorkspacePatchCacheKey(), service.cacheKey)
	}
	if len(service.cacheContent) == 0 {
		t.Fatal("workspace patch was not uploaded")
	}

	var workflow map[string]interface{}
	if err := yaml.Unmarshal([]byte(service.runRequest.GetWorkflowContent()[0]), &workflow); err != nil {
		t.Fatal(err)
	}
	jobs := workflow["jobs"].(map[string]interface{})
	if got := jobs["reusable"].(map[string]interface{}); len(got) != 1 || got["uses"] != "./.depot/workflows/reusable.yml" {
		t.Fatalf("reusable call changed: %#v", got)
	}
	steps := jobs["build"].(map[string]interface{})["steps"].([]interface{})
	if len(steps) != 3 || steps[1].(map[string]interface{})["name"] != "Apply local patch from Depot Cache" {
		t.Fatalf("ordinary job patch steps = %#v", steps)
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
