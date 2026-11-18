package mr

import (
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestReviewPinsHeadAndPreservesRetryKeyAndBody(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	head := strings.Repeat("a", 40)
	current := fixture(head)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return &code.GetMergeRequestResponse{MergeRequest: current}, nil
	})
	calls := 0
	var refusal connect.Code
	rpc(t, mux, codev1beta2connect.MergeRequestServiceSubmitMergeRequestReviewProcedure, func(req *code.SubmitMergeRequestReviewRequest) (*code.SubmitMergeRequestReviewResponse, error) {
		calls++
		if req.MergeRequestId != current.Id || req.ExpectedHeadOid != head || req.RequestKey != "retry-1" || req.Body != "Please fix\nthis" || req.Verdict != code.MergeRequestReview_VERDICT_REQUEST_CHANGES {
			t.Fatalf("wrong review: %v", req)
		}
		if refusal != 0 {
			return nil, connect.NewError(refusal, fmt.Errorf("refused"))
		}
		return &code.SubmitMergeRequestReviewResponse{Review: &code.MergeRequestReview{Id: "mrr_1", MergeRequestId: current.Id, HeadOid: head, Verdict: req.Verdict, Body: req.Body}}, nil
	})
	args := []string{"review", "--id", current.Id, "--request-changes", "--body", "Please fix\nthis", "--request-key", "retry-1", "--json"}
	if out, err := executeWithoutRepo(t, args...); err != nil || !strings.Contains(out, "REQUEST_CHANGES") {
		t.Fatalf("%q, %v", out, err)
	}
	current.HeadOid = strings.Repeat("b", 40)
	refusal = connect.CodeAborted
	out, err := executeWithoutRepo(t, append(args, "--expected-head-oid", head)...)
	if err == nil || !strings.Contains(err.Error(), "--request-key retry-1 --expected-head-oid "+head) || out != "" {
		t.Fatalf("unsafe retry: %q, %v", out, err)
	}
	refusal = connect.CodeUnimplemented
	_, err = executeWithoutRepo(t, append(args, "--expected-head-oid", head)...)
	if connect.CodeOf(err) != connect.CodeUnimplemented || calls != 3 {
		t.Fatalf("fallback or hidden failure: %v, %d", err, calls)
	}
}

func TestReviewHistoryAndWithdrawalWithoutCheckout(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceListMergeRequestReviewsProcedure, func(req *code.ListMergeRequestReviewsRequest) (*code.ListMergeRequestReviewsResponse, error) {
		if req.MergeRequestId != "mr_1" || req.PageToken != "page2" || req.PageSize != 1 {
			t.Fatalf("wrong page: %v", req)
		}
		return &code.ListMergeRequestReviewsResponse{Reviews: []*code.MergeRequestReview{{Id: "mrr_1", MergeRequestId: "mr_1", Verdict: code.MergeRequestReview_VERDICT_REQUEST_CHANGES, Body: "Fix\x1b[31m"}}, NextPageToken: "page3"}, nil
	})
	out, err := executeWithoutRepo(t, "reviews", "--id", "mr_1", "--page-token", "page2", "--limit", "1")
	if err != nil || strings.ContainsRune(out, '\x1b') || !strings.Contains(out, "--page-token page3") {
		t.Fatalf("%q, %v", out, err)
	}
	rpc(t, mux, codev1beta2connect.MergeRequestServiceWithdrawMergeRequestReviewProcedure, func(req *code.WithdrawMergeRequestReviewRequest) (*code.WithdrawMergeRequestReviewResponse, error) {
		return &code.WithdrawMergeRequestReviewResponse{Review: &code.MergeRequestReview{Id: req.ReviewId, MergeRequestId: req.MergeRequestId, WithdrawnAt: timestamppb.Now()}}, nil
	})
	out, err = executeWithoutRepo(t, "withdraw-review", "mrr_1", "--id", "mr_1", "--json")
	if err != nil || !strings.Contains(out, "withdrawnAt") {
		t.Fatalf("%q, %v", out, err)
	}
}
