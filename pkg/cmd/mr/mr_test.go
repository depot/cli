package mr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func runGit(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func checkout(t *testing.T) string {
	t.Helper()
	t.Chdir(t.TempDir())
	runGit(t, "init", "-b", "feature")
	runGit(t, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "initial")
	return runGit(t, "rev-parse", "HEAD")
}

func apiServer(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	return mux
}

func rpc[Req, Resp any](t *testing.T, mux *http.ServeMux, path string, f func(*Req) (*Resp, error)) {
	t.Helper()
	mux.Handle(path, connect.NewUnaryHandler(path, func(_ context.Context, req *connect.Request[Req]) (*connect.Response[Resp], error) {
		if req.Header().Get("Authorization") != "Bearer local-test-token" || req.Header().Get("X-Depot-Org") != "test-org" {
			t.Error("missing authentication or organization header")
		}
		if !strings.HasPrefix(req.Header().Get("User-Agent"), "depot-cli/") {
			t.Error("missing user agent")
		}
		response, err := f(req.Msg)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(response), nil
	}))
}

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewCmdMR()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append(args, "--repo", "repo", "--org", "test-org", "--token", "local-test-token"))
	err := cmd.Execute()
	return out.String(), err
}

func fixture(head string) *code.MergeRequest {
	return &code.MergeRequest{Id: "mr_1", RepositoryId: "repo", Title: "Test change", Body: "Description", State: code.MergeRequest_STATE_OPEN, HeadRef: "feature", BaseRef: "main", HeadOid: head}
}

func getRPC(t *testing.T, mux *http.ServeMux, mr *code.MergeRequest, expectedBranch string) {
	t.Helper()
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if req.GetHead().GetRepositoryId() != "repo" || req.GetHead().GetHeadRef() != expectedBranch {
			t.Errorf("wrong branch lookup: %v", req)
		}
		return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
	})
}

func TestCreateAndViewCurrentBranchInTextAndJSON(t *testing.T) {
	head := checkout(t)
	runGit(t, "tag", "feature")
	mux := apiServer(t)
	mr := fixture(head)
	calls := 0
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(req *code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		if req.RepositoryId != "repo" || req.HeadRef != "feature" || req.Title != "Test change" || req.Body != "Description" {
			t.Errorf("wrong creation request: %v", req)
		}
		calls++
		return &code.CreateMergeRequestResponse{MergeRequest: mr}, nil
	})
	getRPC(t, mux, mr, "feature")
	for _, command := range [][]string{{"create", "--title", "Test change", "--body", "Description"}, {"view"}} {
		out, err := execute(t, command...)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"mr_1", "Test change", "Description", "State: open", head, "Base: main"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q from %s", want, out)
			}
		}
		out, err = execute(t, append(command, "--json")...)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			MergeRequest struct{ ID, HeadOid, State string }
		}
		if err := json.Unmarshal([]byte(out), &response); err != nil {
			t.Fatal(err)
		}
		if response.MergeRequest.ID != "mr_1" || response.MergeRequest.HeadOid != head || response.MergeRequest.State != "STATE_OPEN" {
			t.Fatalf("unexpected JSON: %s", out)
		}
	}
	if calls != 2 {
		t.Fatalf("expected one create call per invocation, got %d", calls)
	}
}

func TestExplicitBranchViewAndCreateWorkWithoutCheckout(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	getRPC(t, mux, fixture("abc"), "feature")
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(req *code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		if req.HeadRef != "feature" {
			t.Errorf("wrong explicit head %q", req.HeadRef)
		}
		return &code.CreateMergeRequestResponse{MergeRequest: fixture("abc")}, nil
	})
	for _, args := range [][]string{{"view", "feature"}, {"create", "--head", "feature", "--title", "Title"}} {
		if _, err := execute(t, args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMergeUsesSelectedLocalBranchAndRefusesStaleHead(t *testing.T) {
	head := checkout(t)
	runGit(t, "checkout", "-b", "other")
	runGit(t, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "different head")
	mux := apiServer(t)
	stale, merged := true, false
	rpc(t, mux, codev1beta2connect.MergeRequestServiceMergeMergeRequestProcedure, func(req *code.MergeMergeRequestRequest) (*code.MergeMergeRequestResponse, error) {
		if req.GetHead().GetHeadRef() != "feature" || req.GetHead().GetRepositoryId() != "repo" || req.GetExpectedHeadOid() != head || req.ExpectedBaseOid != nil {
			t.Errorf("wrong merge preconditions: %v", req)
		}
		if stale {
			return nil, connect.NewError(connect.CodeAborted, errors.New("head branch moved; refresh before merging"))
		}
		merged = true
		mr := fixture(head)
		mr.State, mr.MergeCommitOid = code.MergeRequest_STATE_MERGED, strings.Repeat("a", 40)
		return &code.MergeMergeRequestResponse{MergeRequest: mr}, nil
	})
	out, err := execute(t, "merge", "feature", "--json")
	if connect.CodeOf(err) != connect.CodeAborted || !strings.Contains(err.Error(), "head branch moved") || out != "" || merged {
		t.Fatalf("stale head should fail with no success output: %q %v", out, err)
	}
	stale = false
	out, err = execute(t, "merge", "feature")
	if err != nil || out != "Merged mr_1: "+strings.Repeat("a", 40)+"\n" || !merged {
		t.Fatalf("merge result: %q %v", out, err)
	}
	out, err = execute(t, "merge", "feature", "--json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, "mergeCommitOid") {
		t.Fatalf("merge JSON: %q %v", out, err)
	}
}

func TestMergeRefusalsAreNotRetriedOrReplacedWithSuccess(t *testing.T) {
	head := checkout(t)
	runGit(t, "tag", "feature")
	for _, code := range []connect.Code{connect.CodeFailedPrecondition, connect.CodeUnimplemented, connect.CodePermissionDenied} {
		t.Run(code.String(), func(t *testing.T) {
			mux := apiServer(t)
			calls := 0
			rpc(t, mux, codev1beta2connect.MergeRequestServiceMergeMergeRequestProcedure, func(req *codeMergeRequest) (*codeMergeResponse, error) {
				if req.GetHead().GetHeadRef() != "feature" || req.GetExpectedHeadOid() != head {
					t.Errorf("wrong current branch with matching tag: %v", req)
				}
				calls++
				return nil, connect.NewError(code, errors.New("merge refused"))
			})
			out, err := execute(t, "merge")
			if connect.CodeOf(err) != code || calls != 1 || out != "" || !strings.Contains(err.Error(), "merge refused") {
				t.Fatalf("refusal was not preserved: %q %v (%d calls)", out, err, calls)
			}
		})
	}
}

func TestMergeSquashAsksForOneCommit(t *testing.T) {
	head := checkout(t)
	mux := apiServer(t)
	refuse := connect.CodeFailedPrecondition
	rpc(t, mux, codev1beta2connect.MergeRequestServiceMergeMergeRequestProcedure, func(req *codeMergeRequest) (*codeMergeResponse, error) {
		t.Error("--squash fell back to the merge-commit method")
		return nil, connect.NewError(connect.CodeInternal, errors.New("unexpected"))
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceMergeMergeRequestWithMethodProcedure, func(req *code.MergeMergeRequestWithMethodRequest) (*code.MergeMergeRequestWithMethodResponse, error) {
		if req.Method != code.MergeMergeRequestWithMethodRequest_METHOD_SQUASH || req.GetHead().GetHeadRef() != "feature" || req.GetExpectedHeadOid() != head {
			t.Errorf("wrong squash request: %v", req)
		}
		if refuse != 0 {
			return nil, connect.NewError(refuse, errors.New("Cannot squash: another open merge request targets feature"))
		}
		mr := fixture(head)
		mr.State, mr.MergeCommitOid = code.MergeRequest_STATE_MERGED, strings.Repeat("b", 40)
		return &code.MergeMergeRequestWithMethodResponse{MergeRequest: mr}, nil
	})
	out, err := execute(t, "merge", "feature", "--squash")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "Cannot squash") || out != "" {
		t.Fatalf("refused squash should fail with no success output: %q %v", out, err)
	}
	// A server that predates the method refuses it; the CLI must not retry as a merge commit.
	refuse = connect.CodeUnimplemented
	out, err = execute(t, "merge", "feature", "--squash")
	if connect.CodeOf(err) != connect.CodeUnimplemented || !strings.Contains(err.Error(), "does not support --squash") || out != "" {
		t.Fatalf("an unsupported squash should fail with no success output: %q %v", out, err)
	}
	refuse = 0
	out, err = execute(t, "merge", "feature", "--squash")
	if err != nil || out != "Squashed and merged mr_1: "+strings.Repeat("b", 40)+"\n" {
		t.Fatalf("squash result: %q %v", out, err)
	}
	out, err = execute(t, "merge", "feature", "--squash", "--json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, "mergeCommitOid") {
		t.Fatalf("squash JSON: %q %v", out, err)
	}
}

type codeMergeRequest = code.MergeMergeRequestRequest
type codeMergeResponse = code.MergeMergeRequestResponse

func TestLocalMergeValidationMakesNoAPICall(t *testing.T) {
	checkout(t)
	runGit(t, "checkout", "--detach")
	mux := apiServer(t)
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("unexpected API request") })
	for _, args := range [][]string{{"merge"}, {"merge", "missing"}, {"merge", "feature~1"}, {"merge", "refs/heads/feature"}, {"create", "--title", ""}, {"comment", "feature", "--body", " "}} {
		if out, err := execute(t, args...); err == nil || out != "" {
			t.Errorf("%v: expected local refusal, got %q %v", args, out, err)
		}
	}
}

func TestApproveAndCommentUseResolvedMRAndSupportJSON(t *testing.T) {
	head := checkout(t)
	runGit(t, "tag", "feature")
	mux := apiServer(t)
	getRPC(t, mux, fixture(head), "feature")
	rpc(t, mux, codev1beta2connect.MergeRequestServiceApproveMergeRequestProcedure, func(req *code.ApproveMergeRequestRequest) (*code.ApproveMergeRequestResponse, error) {
		if req.MergeRequestId != "mr_1" {
			t.Errorf("wrong MR %q", req.MergeRequestId)
		}
		return &code.ApproveMergeRequestResponse{Approval: &code.MergeRequestApproval{MergeRequestId: "mr_1", HeadOid: head}}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestCommentProcedure, func(req *code.CreateMergeRequestCommentRequest) (*code.CreateMergeRequestCommentResponse, error) {
		if req.MergeRequestId != "mr_1" || req.Body != "Looks good\nNext line" {
			t.Errorf("wrong comment: %v", req)
		}
		return &code.CreateMergeRequestCommentResponse{Comment: &code.MergeRequestComment{Id: "mrc_1", MergeRequestId: req.MergeRequestId, Body: req.Body}}, nil
	})
	for _, args := range [][]string{{"approve"}, {"comment", "--body", "Looks good\nNext line"}} {
		out, err := execute(t, args...)
		if err != nil || !strings.Contains(out, "mr_1") {
			t.Fatalf("%v: %s %v", args, out, err)
		}
		out, err = execute(t, append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, "mergeRequestId") {
			t.Fatalf("%v JSON: %s %v", args, out, err)
		}
	}
}

func TestFailedLookupCannotApproveOrComment(t *testing.T) {
	checkout(t)
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
	})
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("attempted mutation after failed lookup") })
	for _, args := range [][]string{{"approve"}, {"comment", "--body", "Body"}} {
		out, err := execute(t, args...)
		if connect.CodeOf(err) != connect.CodePermissionDenied || out != "" {
			t.Fatalf("%v: %q %v", args, out, err)
		}
	}
}
