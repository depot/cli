package mr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
	"github.com/depot/cli/pkg/proto/depot/git/v1/gitv1connect"
)

func checkoutFixture(t *testing.T, remotes ...string) (*code.MergeRequest, string) {
	t.Helper()
	base := checkout(t)
	runGit(t, "branch", "main")
	if err := os.WriteFile("change.txt", []byte("reviewed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "change.txt")
	runGit(t, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "reviewed")
	head := runGit(t, "rev-parse", "HEAD")
	source := filepath.Join(t.TempDir(), "source.git")
	runGit(t, "clone", "--bare", ".", source)
	runGit(t, "checkout", "main")
	runGit(t, "branch", "-D", "feature")
	if len(remotes) == 0 {
		remotes = []string{"https://test-org.code.depot.dev/old-name"}
	}
	for i, remote := range remotes {
		runGit(t, "remote", "add", fmt.Sprintf("remote%d", i), remote)
	}
	expectedURL := remotes[0]
	if strings.Contains(expectedURL, "github.com") {
		expectedURL = "https://test-org.code.depot.dev/widgets"
	}
	checkoutFetchTransport(t, source, expectedURL)
	mux := apiServer(t)
	rpc(t, mux, gitv1connect.RepositoryServiceResolveProcedure, func(req *gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
		if req.Repository.GetName() != "old-name" && req.Repository.GetId() != "repo" {
			t.Errorf("unexpected repository %v", req)
		}
		return &gitv1.ResolveResponse{Repository: mirror("repo", "acme/widgets")}, nil
	})
	rpc(t, mux, codev1beta2connect.CodeServiceGetRepositoryProcedure, func(req *code.GetRepositoryRequest) (*code.GetRepositoryResponse, error) {
		if req.GetRepositoryId() != "repo" {
			t.Errorf("unexpected repository %v", req)
		}
		return &code.GetRepositoryResponse{Repository: &code.Repository{RepositoryId: "repo", Name: "widgets", DefaultBranch: "main"}}, nil
	})
	rpc(t, mux, gitv1connect.RepositoryServiceListProcedure, func(*gitv1.ListRequest) (*gitv1.ListResponse, error) {
		t.Error("checkout with a known repository must not require owner-only listing")
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner access required"))
	})
	mr := fixture(head)
	mr.HeadRef = "feature/review"
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if req.GetMergeRequestId() != "mr_1" && (req.GetHead().GetRepositoryId() != "repo" || req.GetHead().GetHeadRef() != mr.HeadRef) {
			t.Errorf("expected stable ID lookup %v", req)
		}
		return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
	})
	return mr, base
}

func TestCheckoutRecordedHeadAndPreserveLocalCommits(t *testing.T) {
	mr, base := checkoutFixture(t)
	runGit(t, "reflog", "expire", "--expire=now", "--all")
	runGit(t, "prune", "--expire=now")
	out, err := execute(t, "checkout", "--id", "mr_1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		HeadOID string `json:"headOid"`
		Branch  string `json:"branch"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.HeadOID != mr.HeadOid || result.Branch != mr.HeadRef || runGit(t, "rev-parse", "HEAD") != mr.HeadOid || runGit(t, "rev-parse", "main") != base {
		t.Fatalf("wrong checkout: %s", out)
	}
	runGit(t, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "local work")
	local := runGit(t, "rev-parse", "HEAD")
	runGit(t, "checkout", "main")
	if _, err := execute(t, "checkout", "--id", "mr_1"); err == nil || !strings.Contains(err.Error(), "local branch has commits") {
		t.Fatalf("expected local commit refusal: %v", err)
	}
	if runGit(t, "rev-parse", mr.HeadRef) != local || runGit(t, "branch", "--show-current") != "main" {
		t.Fatal("refusal changed local branches")
	}
	if _, err := execute(t, "checkout", "--id", "mr_1", "--force"); err != nil {
		t.Fatal(err)
	}
	if runGit(t, "rev-parse", "HEAD") != mr.HeadOid {
		t.Fatal("force did not reset branch")
	}
}

func TestCheckoutRefusalsPreserveWork(t *testing.T) {
	for _, kind := range []string{"dirty", "ignored collision", "in-progress rebase", "wrong repository", "wrong ID", "deleted", "closed", "merged", "default branch", "case default branch", "case branch override", "unrelated GitHub", "wrong organization", "invalid head", "other worktree"} {
		t.Run(kind, func(t *testing.T) {
			mr, base := checkoutFixture(t)
			args := []string{"checkout", "--id", "mr_1", "--force"}
			switch kind {
			case "dirty":
				if err := os.WriteFile("notes.txt", []byte("keep me"), 0600); err != nil {
					t.Fatal(err)
				}
			case "ignored collision":
				if err := os.WriteFile(".git/info/exclude", []byte("change.txt\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile("change.txt", []byte("keep ignored"), 0600); err != nil {
					t.Fatal(err)
				}
			case "in-progress rebase":
				if err := os.Mkdir(".git/rebase-merge", 0700); err != nil {
					t.Fatal(err)
				}
			case "wrong repository":
				mr.RepositoryId = "other"
			case "wrong ID":
				mr.Id = "mr_other"
			case "deleted":
				mr.HeadBranchDeleted = true
			case "closed":
				mr.State = code.MergeRequest_STATE_CLOSED
			case "merged":
				mr.State = code.MergeRequest_STATE_MERGED
			case "default branch":
				mr.HeadRef = "main"
			case "case default branch":
				mr.HeadRef = "MAIN"
			case "case branch override":
				args = append(args, "--branch", "Main")
			case "unrelated GitHub":
				runGit(t, "remote", "set-url", "remote0", "https://github.com/unrelated/widgets")
			case "wrong organization":
				runGit(t, "remote", "set-url", "remote0", "https://other-org.code.depot.dev/old-name")
			case "invalid head":
				mr.HeadOid = "--upload-pack=evil"
			case "other worktree":
				runGit(t, "worktree", "add", "-b", mr.HeadRef, filepath.Join(t.TempDir(), "work"), base)
			}
			if out, err := execute(t, args...); err == nil || out != "" {
				t.Fatalf("expected refusal: %s %v", out, err)
			}
			if runGit(t, "rev-parse", "HEAD") != base || runGit(t, "branch", "--show-current") != "main" {
				t.Fatal("refusal changed checkout")
			}
			if kind == "ignored collision" {
				b, err := os.ReadFile("change.txt")
				if err != nil || string(b) != "keep ignored" {
					t.Fatal("lost ignored changes")
				}
			}
			if kind == "dirty" {
				b, err := os.ReadFile("notes.txt")
				if err != nil || string(b) != "keep me" {
					t.Fatal("lost local changes")
				}
			}
		})
	}
}

func TestCheckoutSettledMRUsesRecordedCommitAfterBranchReuse(t *testing.T) {
	mr, _ := checkoutFixture(t)
	mr.State = code.MergeRequest_STATE_MERGED
	mr.HeadBranchDeleted = true
	runGit(t, "checkout", "-b", mr.HeadRef)
	runGit(t, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "reused branch")
	reused := runGit(t, "rev-parse", "HEAD")
	if _, err := execute(t, "checkout", "--id", "mr_1", "--detach"); err != nil {
		t.Fatal(err)
	}
	if runGit(t, "branch", "--show-current") != "" || runGit(t, "rev-parse", "HEAD") != mr.HeadOid || runGit(t, "rev-parse", mr.HeadRef) != reused {
		t.Fatal("did not detach at historical MR commit")
	}
	mr.HeadOid = strings.Repeat("f", 40)
	if out, err := execute(t, "checkout", "--id", "mr_1", "--detach"); err == nil || out != "" {
		t.Fatalf("missing object must fail: %s %v", out, err)
	}
}

func TestCheckoutUsesLocalRecordedCommitWithoutFetch(t *testing.T) {
	mr, base := checkoutFixture(t)
	runGit(t, "config", "fetch.recurseSubmodules", "invalid")
	if _, err := execute(t, "checkout", "--id", "mr_1"); err != nil {
		t.Fatalf("local recorded commit should not depend on fetch: %v", err)
	}
	if runGit(t, "rev-parse", "HEAD") != mr.HeadOid || runGit(t, "rev-parse", "main") != base {
		t.Fatal("did not check out the recorded local commit")
	}
	mr.HeadOid = strings.Repeat("f", 40)
	before := runGit(t, "rev-parse", "HEAD")
	if _, err := execute(t, "checkout", "--id", "mr_1"); err == nil || !strings.Contains(err.Error(), "cannot fetch") {
		t.Fatalf("missing commit must still require a successful fetch: %v", err)
	}
	if runGit(t, "rev-parse", "HEAD") != before {
		t.Fatal("failed fetch changed checkout")
	}
}

// Redirect only fetch's transport to a local bare repo, preserving real Git's ref and checkout behavior.
func checkoutFetchTransport(t *testing.T, source, expectedURL string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = fetch ]; then
  matched=false
  for arg in "$@"; do
    if [ "$arg" = "$DEPOT_TEST_FETCH_URL" ]; then matched=true; fi
  done
  if [ "$matched" != true ]; then exit 1; fi
  exec "$DEPOT_TEST_REAL_GIT" -c "url.$DEPOT_TEST_SOURCE.insteadOf=$DEPOT_TEST_FETCH_URL" "$@"
fi
exec "$DEPOT_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEPOT_TEST_REAL_GIT", realGit)
	t.Setenv("DEPOT_TEST_SOURCE", source)
	t.Setenv("DEPOT_TEST_FETCH_URL", expectedURL)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCheckoutFetchUsesConfiguredDepotTransport(t *testing.T) {
	for _, remote := range []string{"git@test-org.code.depot.dev:old-name", "ssh://git@test-org.code.depot.dev:2222/old-name", "https://user:secret@code.depot.dev/old-name"} {
		t.Run(remote, func(t *testing.T) {
			mr, base := checkoutFixture(t, remote)
			runGit(t, "reflog", "expire", "--expire=now", "--all")
			runGit(t, "prune", "--expire=now")
			if out, err := execute(t, "checkout", "--id", "mr_1"); err != nil || strings.Contains(out, "secret") {
				t.Fatalf("checkout using configured transport: %q %v", out, err)
			}
			if runGit(t, "rev-parse", "HEAD") != mr.HeadOid || runGit(t, "rev-parse", "main") != base {
				t.Fatal("did not fetch and check out recorded commit")
			}
		})
	}
}

func TestCheckoutGitHubMirrorWithKnownRepositoryDoesNotList(t *testing.T) {
	for _, selector := range [][]string{{"--id", "mr_1"}, {"feature/review", "--repo", "repo"}} {
		t.Run(strings.Join(selector, " "), func(t *testing.T) {
			mr, base := checkoutFixture(t, "git@github.com:acme/widgets.git", "https://github.com/personal/widgets")
			runGit(t, "reflog", "expire", "--expire=now", "--all")
			runGit(t, "prune", "--expire=now")
			if _, err := executeWithoutRepo(t, append([]string{"checkout"}, selector...)...); err != nil {
				t.Fatal(err)
			}
			if runGit(t, "rev-parse", "HEAD") != mr.HeadOid || runGit(t, "rev-parse", "main") != base {
				t.Fatal("did not fetch and check out recorded mirror commit")
			}
		})
	}
}
