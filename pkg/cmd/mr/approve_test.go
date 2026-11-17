package mr

import (
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
)

func TestApprovalAndWithdrawalWithoutCheckout(t *testing.T) {
	t.Chdir(t.TempDir())
	head := strings.Repeat("a", 40)
	mux := apiServer(t)
	mr := fixture(head)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if req.GetMergeRequestId() != mr.Id {
			t.Fatalf("wrong lookup: %v", req)
		}
		return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
	})
	approvals, withdrawals, unguardedApprovals := 0, 0, 0
	var refuse connect.Code
	result := &code.MergeRequestApproval{MergeRequestId: mr.Id, HeadOid: head}
	rpc(t, mux, codev1beta2connect.MergeRequestServiceApproveMergeRequestAtRevisionProcedure, func(req *code.ApproveMergeRequestAtRevisionRequest) (*code.ApproveMergeRequestAtRevisionResponse, error) {
		approvals++
		if req.MergeRequestId != mr.Id || req.ExpectedHeadOid != head {
			t.Fatalf("wrong guarded request: %v", req)
		}
		if refuse != 0 {
			return nil, connect.NewError(refuse, fmt.Errorf("refused"))
		}
		return &code.ApproveMergeRequestAtRevisionResponse{Approval: result}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceApproveMergeRequestProcedure, func(req *code.ApproveMergeRequestRequest) (*code.ApproveMergeRequestResponse, error) {
		unguardedApprovals++
		if req.MergeRequestId != mr.Id {
			t.Fatalf("wrong approval: %v", req)
		}
		return &code.ApproveMergeRequestResponse{Approval: result}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceRemoveMergeRequestApprovalProcedure, func(req *code.RemoveMergeRequestApprovalRequest) (*code.RemoveMergeRequestApprovalResponse, error) {
		withdrawals++
		if req.MergeRequestId != mr.Id {
			t.Fatalf("wrong withdrawal: %v", req)
		}
		return &code.RemoveMergeRequestApprovalResponse{}, nil
	})
	args := []string{"approve", "--id", mr.Id, "--expected-head-oid", head}
	for _, json := range []bool{false, true} {
		invocation := append([]string{}, args...)
		if json {
			invocation = append(invocation, "--json")
		}
		out, err := execute(t, invocation...)
		if err != nil || !strings.Contains(out, head) {
			t.Fatalf("approval: %q, %v", out, err)
		}
	}
	for _, refuse = range []connect.Code{connect.CodeAborted, connect.CodePermissionDenied, connect.CodeNotFound, connect.CodeUnimplemented} {
		out, err := execute(t, args...)
		if connect.CodeOf(err) != refuse || out != "" {
			t.Fatalf("refusal %v: %q, %v", refuse, out, err)
		}
		if refuse == connect.CodeUnimplemented && !strings.Contains(err.Error(), "nothing was approved") {
			t.Fatal(err)
		}
	}
	refuse = 0
	result.HeadOid = strings.Repeat("b", 40)
	if _, err := execute(t, args...); err == nil {
		t.Fatal("accepted a different approval head")
	}
	for _, state := range []code.MergeRequest_State{code.MergeRequest_STATE_MERGED, code.MergeRequest_STATE_CLOSED} {
		mr.State = state
		out, err := execute(t, args...)
		if err == nil || !strings.Contains(err.Error(), mr.Id) || !strings.Contains(err.Error(), stateName(state)) || strings.Contains(err.Error(), "branch") || out != "" {
			t.Fatalf("non-open approval by ID: %q, %v", out, err)
		}
	}
	for _, extra := range [][]string{nil, {"--json"}} {
		out, err := execute(t, append([]string{"approve", "--id", mr.Id, "--withdraw"}, extra...)...)
		if err != nil || out == "" {
			t.Fatalf("withdraw: %q, %v", out, err)
		}
	}
	if withdrawals != 2 || approvals != 7 {
		t.Fatalf("wrong mutation counts: approve %d, withdraw %d", approvals, withdrawals)
	}
	mr.RepositoryId = "other-repo"
	for _, command := range [][]string{args, {"approve", "--id", mr.Id, "--withdraw"}} {
		if _, err := execute(t, command...); err == nil {
			t.Fatal("accepted wrong repository")
		}
	}
	if withdrawals != 2 || approvals != 7 {
		t.Fatal("mutated the wrong repository")
	}
	if unguardedApprovals != 0 {
		t.Fatal("guarded approval fell back to current-head approval")
	}
	mr.State, mr.RepositoryId = code.MergeRequest_STATE_OPEN, "repo"
	for _, extra := range [][]string{nil, {"--json"}} {
		out, err := execute(t, append([]string{"approve", "--id", mr.Id}, extra...)...)
		if err != nil || !strings.Contains(out, result.HeadOid) {
			t.Fatalf("unguarded approval: %q, %v", out, err)
		}
	}
	if unguardedApprovals != 2 || approvals != 7 {
		t.Fatalf("wrong approval counts: guarded %d, unguarded %d", approvals, unguardedApprovals)
	}
}

func TestApprovalFlagsFailBeforeCallingAPI(t *testing.T) {
	t.Chdir(t.TempDir())
	apiServer(t)
	for _, args := range [][]string{
		{"approve", "--id", "mr_1", "--expected-head-oid", "abc"},
		{"approve", "feature", "--expected-head-oid", ""},
		{"approve", "feature", "--id", "mr_1", "--withdraw"},
		{"approve", "--id", "mr_1", "--withdraw", "--expected-head-oid", strings.Repeat("a", 40)},
	} {
		if _, err := execute(t, args...); err == nil || strings.Contains(err.Error(), "unimplemented") {
			t.Fatalf("bad flags %v: %v", args, err)
		}
	}
}
