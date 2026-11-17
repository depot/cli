package mr

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
)

func TestMergeByIDWithoutCheckoutPreservesRevisionGuards(t *testing.T) {
	for _, squash := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "squash"}[squash], func(t *testing.T) {
			t.Chdir(t.TempDir())
			mux := apiServer(t)
			head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
			mr := fixture(head)
			rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
				if req.GetMergeRequestId() != mr.Id || req.GetHead() != nil {
					t.Errorf("must look up the exact MR even when its branch has been reused: %v", req)
				}
				return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
			})
			calls := 0
			refusal := ""
			refusalCode := connect.CodeAborted
			rpc(t, mux, codev1beta2connect.MergeRequestServiceMergeMergeRequestWithMethodProcedure, func(req *code.MergeMergeRequestWithMethodRequest) (*code.MergeMergeRequestWithMethodResponse, error) {
				calls++
				method := code.MergeMergeRequestWithMethodRequest_METHOD_MERGE_COMMIT
				if squash {
					method = code.MergeMergeRequestWithMethodRequest_METHOD_SQUASH
				}
				if req.GetMergeRequestId() != mr.Id || req.GetHead() != nil || req.GetExpectedHeadOid() != head || req.GetExpectedBaseOid() != base || req.GetExpectedBaseRef() != "main" || req.Method != method {
					t.Errorf("lost identity or revision guards: %v", req)
				}
				if refusal != "" {
					return nil, connect.NewError(refusalCode, errors.New(refusal))
				}
				merged := fixture(head)
				merged.State, merged.MergeCommitOid = code.MergeRequest_STATE_MERGED, strings.Repeat("c", 40)
				return &code.MergeMergeRequestWithMethodResponse{MergeRequest: merged}, nil
			})
			args := []string{"merge", "--id", mr.Id, "--expected-head-oid", head, "--expected-base-oid", base, "--expected-base-ref", "main", "--json"}
			if squash {
				args = append(args, "--squash")
			}
			out, err := execute(t, args...)
			var response struct {
				MergeRequest struct{ ID, State, MergeCommitOid string }
			}
			if err != nil || json.Unmarshal([]byte(out), &response) != nil || response.MergeRequest.ID != mr.Id || response.MergeRequest.State != "STATE_MERGED" || response.MergeRequest.MergeCommitOid != strings.Repeat("c", 40) {
				t.Fatalf("authoritative merge result: %q %v", out, err)
			}
			for _, refusal = range []string{"Head branch moved", "Base branch moved", "Base branch changed"} {
				before := calls
				out, err = execute(t, args...)
				if connect.CodeOf(err) != connect.CodeAborted || !strings.Contains(err.Error(), refusal) || out != "" || calls != before+1 {
					t.Fatalf("must preserve refusal without retry: %q %v (%d calls)", out, err, calls-before)
				}
			}
			for _, refusalCode = range []connect.Code{connect.CodeFailedPrecondition, connect.CodeNotFound, connect.CodePermissionDenied, connect.CodeUnimplemented} {
				refusal = "merge refused"
				before := calls
				out, err = execute(t, args...)
				if connect.CodeOf(err) != refusalCode || out != "" || calls != before+1 {
					t.Fatalf("must not fall back to a branch merge: %q %v (%d calls)", out, err, calls-before)
				}
			}
		})
	}
}

func TestMergeByIDWithoutRepositoryUsesSHA256AndServerObservedBase(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	head := strings.Repeat("a", 64)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceMergeMergeRequestWithMethodProcedure, func(req *code.MergeMergeRequestWithMethodRequest) (*code.MergeMergeRequestWithMethodResponse, error) {
		if req.GetMergeRequestId() != "mr_1" || req.ExpectedBaseOid != nil || req.GetExpectedHeadOid() != head || req.GetExpectedBaseRef() != "main" {
			t.Errorf("wrong revision guards: %v", req)
		}
		mr := fixture(head)
		mr.State, mr.MergeCommitOid = code.MergeRequest_STATE_MERGED, strings.Repeat("b", 64)
		return &code.MergeMergeRequestWithMethodResponse{MergeRequest: mr}, nil
	})
	cmd := NewCmdMR()
	cmd.SetArgs([]string{"merge", "--id", "mr_1", "--expected-head-oid", head, "--expected-base-ref", "main", "--org", "test-org", "--token", "local-test-token"})
	var out strings.Builder
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), "Merged mr_1") {
		t.Fatalf("merge without a local repository: %q %v", out.String(), err)
	}
}

func TestMergeByIDRejectsWrongRepositoryBeforeMutation(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	mr := fixture(strings.Repeat("a", 40))
	mr.RepositoryId = "other"
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
	})
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("must not mutate another repository") })
	out, err := execute(t, "merge", "--id", mr.Id, "--expected-head-oid", mr.HeadOid, "--expected-base-ref", "main")
	if err == nil || !strings.Contains(err.Error(), "repository") || out != "" {
		t.Fatalf("wrong repository: %q %v", out, err)
	}
}

func TestMergeRevisionValidationBeforeAPI(t *testing.T) {
	checkout(t)
	mux := apiServer(t)
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("unexpected API request") })
	head := strings.Repeat("a", 40)
	for _, args := range [][]string{
		{"--id", ""},
		{"feature", "--id", "mr_1"},
		{"--id", "mr_1"},
		{"--id", "mr_1", "--expected-head-oid", head},
		{"--id", "mr_1", "--expected-head-oid", "abc", "--expected-base-ref", "main"},
		{"--id", "mr_1", "--expected-head-oid", head, "--expected-base-ref", ""},
		{"--id", "mr_1", "--expected-head-oid", head, "--expected-base-ref", "main", "--expected-base-oid", ""},
		{"--expected-head-oid", head},
	} {
		if out, err := execute(t, append([]string{"merge"}, args...)...); err == nil || out != "" {
			t.Errorf("%v: expected validation error, got %q %v", args, out, err)
		}
	}
}
