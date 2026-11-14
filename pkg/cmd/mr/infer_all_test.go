package mr

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
	"github.com/depot/cli/pkg/proto/depot/git/v1/gitv1connect"
)

func executeWithoutRepo(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewCmdMR()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append(args, "--org", "test-org", "--token", "local-test-token"))
	err := cmd.Execute()
	return out.String(), err
}

// mrServer answers every merge request RPC and records the repository IDs the lookups carried.
func mrServer(t *testing.T, mux *http.ServeMux, head string) *[]string {
	t.Helper()
	var repos []string
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		repos = append(repos, req.GetHead().GetRepositoryId())
		return &code.GetMergeRequestResponse{MergeRequest: fixture(head)}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceListMergeRequestsProcedure, func(req *code.ListMergeRequestsRequest) (*code.ListMergeRequestsResponse, error) {
		repos = append(repos, req.RepositoryId)
		return &code.ListMergeRequestsResponse{MergeRequests: []*code.MergeRequest{fixture(head)}}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceMergeMergeRequestProcedure, func(req *code.MergeMergeRequestRequest) (*code.MergeMergeRequestResponse, error) {
		repos = append(repos, req.GetHead().GetRepositoryId())
		mr := fixture(head)
		mr.State, mr.MergeCommitOid = code.MergeRequest_STATE_MERGED, strings.Repeat("a", 40)
		return &code.MergeMergeRequestResponse{MergeRequest: mr}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceApproveMergeRequestProcedure, func(req *code.ApproveMergeRequestRequest) (*code.ApproveMergeRequestResponse, error) {
		return &code.ApproveMergeRequestResponse{Approval: &code.MergeRequestApproval{MergeRequestId: req.MergeRequestId, HeadOid: head}}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestCommentProcedure, func(req *code.CreateMergeRequestCommentRequest) (*code.CreateMergeRequestCommentResponse, error) {
		return &code.CreateMergeRequestCommentResponse{Comment: &code.MergeRequestComment{Id: "mrc_1", MergeRequestId: req.MergeRequestId, Body: req.Body}}, nil
	})
	updateRPC(t, mux, func(req *code.UpdateMergeRequestRequest) *code.MergeRequest {
		mr := fixture(head)
		mr.Id = req.MergeRequestId
		if req.State != code.MergeRequest_STATE_UNSPECIFIED {
			mr.State = req.State
		}
		return mr
	})
	return &repos
}

var branchCommands = [][]string{
	{"view"}, {"list"}, {"merge"}, {"approve"}, {"comment", "--body", "Looks good"},
	{"edit", "--title", "Renamed"}, {"close"},
}

func TestEveryCommandInfersTheRepositoryFromRemotes(t *testing.T) {
	head := checkout(t)
	runGit(t, "remote", "add", "origin", "https://code.depot.dev/widgets")
	mux := apiServer(t)
	resolves := 0
	rpc(t, mux, gitv1connect.RepositoryServiceResolveProcedure, func(req *gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
		if req.Repository.GetName() != "widgets" {
			t.Errorf("wrong name selector: %v", req)
		}
		resolves++
		return &gitv1.ResolveResponse{Repository: &gitv1.Repository{Id: "opaque-id", OrgId: "test-org"}}, nil
	})
	repos := mrServer(t, mux, head)
	for i, args := range branchCommands {
		out, err := executeWithoutRepo(t, args...)
		if err != nil || !strings.Contains(out, "mr_1") {
			t.Fatalf("%v: %q %v", args, out, err)
		}
		if resolves != i+1 || len(*repos) != i+1 || (*repos)[i] != "opaque-id" {
			t.Fatalf("%v: %d resolves, lookups used %v", args, resolves, *repos)
		}
	}
	// An explicit --repo wins and skips the remotes.
	if _, err := executeWithoutRepo(t, "view", "--repo", "explicit"); err != nil || resolves != len(branchCommands) || (*repos)[len(*repos)-1] != "explicit" {
		t.Fatalf("explicit --repo: %v, %d resolves, lookups used %v", err, resolves, *repos)
	}
}

func TestCommandsRefuseToGuessTheRepository(t *testing.T) {
	checkout(t)
	mux := apiServer(t)
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("unexpected API request") })
	for _, args := range append([][]string{{"view", "--repo", " "}, {"reopen", "--id", "mr_9", "--repo", ""}}, branchCommands...) {
		out, err := executeWithoutRepo(t, args...)
		if err == nil || !strings.Contains(err.Error(), "--repo") || out != "" {
			t.Fatalf("%v should ask for --repo: %q %v", args, out, err)
		}
	}
}

func TestIDCommandsNeedNoRepositoryOrCheckout(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	mrServer(t, mux, "abc")
	for _, args := range [][]string{{"reopen", "--id", "mr_9"}, {"close", "--id", "mr_9"}, {"edit", "--id", "mr_9", "--title", "Renamed"}} {
		if out, err := executeWithoutRepo(t, args...); err != nil || !strings.Contains(out, "mr_9") {
			t.Fatalf("%v: %q %v", args, out, err)
		}
	}
	if out, err := executeWithoutRepo(t, "threads", "--id", "mr_9", "--json"); err != nil || !strings.Contains(out, `"threads": []`) {
		t.Fatalf("threads: %q %v", out, err)
	}
}
