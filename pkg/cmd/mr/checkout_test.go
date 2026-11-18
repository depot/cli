package mr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
	"github.com/depot/cli/pkg/proto/depot/git/v1/gitv1connect"
)

func checkoutFixture(t *testing.T) (*code.MergeRequest, string) {
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
	runGit(t, "remote", "add", "origin", "https://test-org.code.depot.dev/old-name")
	runGit(t, "config", "url."+source+".insteadOf", "https://test-org.code.depot.dev/widgets")
	mux := apiServer(t)
	rpc(t, mux, gitv1connect.RepositoryServiceResolveProcedure, func(req *gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
		if req.Repository.GetName() != "old-name" {
			t.Errorf("unexpected repository %v", req)
		}
		return &gitv1.ResolveResponse{Repository: &gitv1.Repository{Id: "repo", OrgId: "test-org", CanonicalName: "widgets"}}, nil
	})
	rpc(t, mux, codev1beta2connect.CodeServiceGetRepositoryProcedure, func(req *code.GetRepositoryRequest) (*code.GetRepositoryResponse, error) {
		if req.GetRepositoryId() != "repo" {
			t.Errorf("unexpected repository %v", req)
		}
		return &code.GetRepositoryResponse{Repository: &code.Repository{RepositoryId: "repo", Name: "widgets", DefaultBranch: "main"}}, nil
	})
	mr := fixture(head)
	mr.HeadRef = "feature/review"
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if req.GetMergeRequestId() != "mr_1" {
			t.Errorf("expected stable ID lookup %v", req)
		}
		return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
	})
	return mr, base
}

func TestCheckoutRecordedHeadAndPreserveLocalCommits(t *testing.T) {
	mr, base := checkoutFixture(t)
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
	for _, kind := range []string{"dirty", "ignored collision", "in-progress rebase", "wrong repository", "wrong ID", "deleted", "closed", "merged", "default branch", "invalid head", "other worktree"} {
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
