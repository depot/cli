package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
)

type ciServiceTestHandler struct {
	t *testing.T
}

func (h ciServiceTestHandler) Run(context.Context, *connect.Request[civ1.RunRequest]) (*connect.Response[civ1.RunResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) DispatchWorkflow(context.Context, *connect.Request[civ1.DispatchWorkflowRequest]) (*connect.Response[civ1.DispatchWorkflowResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) RetryJob(context.Context, *connect.Request[civ1.RetryJobRequest]) (*connect.Response[civ1.RetryJobResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) RerunWorkflow(context.Context, *connect.Request[civ1.RerunWorkflowRequest]) (*connect.Response[civ1.RerunWorkflowResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) RetryFailedJobs(context.Context, *connect.Request[civ1.RetryFailedJobsRequest]) (*connect.Response[civ1.RetryFailedJobsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) CancelJob(context.Context, *connect.Request[civ1.CancelJobRequest]) (*connect.Response[civ1.CancelJobResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) CancelWorkflow(context.Context, *connect.Request[civ1.CancelWorkflowRequest]) (*connect.Response[civ1.CancelWorkflowResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) GetRun(_ context.Context, req *connect.Request[civ1.GetRunRequest]) (*connect.Response[civ1.GetRunResponse], error) {
	assertAuthAndOrg(h.t, req.Header())
	if req.Msg.RunId != "run-123" {
		h.t.Fatalf("RunId = %q, want run-123", req.Msg.RunId)
	}
	return connect.NewResponse(&civ1.GetRunResponse{RunId: req.Msg.RunId, OrgId: "org-123"}), nil
}

func (h ciServiceTestHandler) CancelRun(_ context.Context, req *connect.Request[civ1.CancelRunRequest]) (*connect.Response[civ1.CancelRunResponse], error) {
	assertAuthAndOrg(h.t, req.Header())
	if req.Msg.RunId != "run-123" {
		h.t.Fatalf("RunId = %q, want run-123", req.Msg.RunId)
	}
	return connect.NewResponse(&civ1.CancelRunResponse{RunId: req.Msg.RunId, Status: "cancelled"}), nil
}

func (h ciServiceTestHandler) GetRunStatus(context.Context, *connect.Request[civ1.GetRunStatusRequest]) (*connect.Response[civ1.GetRunStatusResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) GetJobAttemptLogs(context.Context, *connect.Request[civ1.GetJobAttemptLogsRequest]) (*connect.Response[civ1.GetJobAttemptLogsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) ListRuns(context.Context, *connect.Request[civ1.ListRunsRequest]) (*connect.Response[civ1.ListRunsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func TestCIGetRunWrapper(t *testing.T) {
	withTestCIService(t, func() {
		resp, err := CIGetRun(context.Background(), "token-123", "org-123", "run-123")
		if err != nil {
			t.Fatalf("CIGetRun returned error: %v", err)
		}
		if resp.RunId != "run-123" || resp.OrgId != "org-123" {
			t.Fatalf("unexpected response: %+v", resp)
		}
	})
}

func TestCICancelRunWrapper(t *testing.T) {
	withTestCIService(t, func() {
		resp, err := CICancelRun(context.Background(), "token-123", "org-123", "run-123")
		if err != nil {
			t.Fatalf("CICancelRun returned error: %v", err)
		}
		if resp.RunId != "run-123" || resp.Status != "cancelled" {
			t.Fatalf("unexpected response: %+v", resp)
		}
	})
}

func withTestCIService(t *testing.T, fn func()) {
	t.Helper()

	mux := http.NewServeMux()
	path, handler := civ1connect.NewCIServiceHandler(ciServiceTestHandler{t: t})
	mux.Handle(path, handler)

	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	defer server.Close()

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	fn()
}

func assertAuthAndOrg(t *testing.T, header http.Header) {
	t.Helper()

	if got := header.Get("Authorization"); got != "Bearer token-123" {
		t.Fatalf("Authorization = %q, want Bearer token-123", got)
	}
	if got := header.Get("x-depot-org"); got != "org-123" {
		t.Fatalf("x-depot-org = %q, want org-123", got)
	}
}

type listRunsRecorder struct {
	civ1connect.UnimplementedCIServiceHandler
	requests []*civ1.ListRunsRequest
}

func (r *listRunsRecorder) ListRuns(ctx context.Context, req *connect.Request[civ1.ListRunsRequest]) (*connect.Response[civ1.ListRunsResponse], error) {
	r.requests = append(r.requests, proto.Clone(req.Msg).(*civ1.ListRunsRequest))

	resp := &civ1.ListRunsResponse{
		Runs: []*civ1.ListRunsResponseRun{
			{RunId: "run-1"},
		},
	}
	if len(r.requests) == 1 {
		resp.NextPageToken = "next"
	}
	return connect.NewResponse(resp), nil
}

func TestCIListRunsPassesFiltersAcrossPages(t *testing.T) {
	recorder := &listRunsRecorder{}
	_, handler := civ1connect.NewCIServiceHandler(recorder)
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	runs, err := CIListRuns(context.Background(), "token", "org-123", CIListRunsOptions{
		Statuses: []string{"failed"},
		Limit:    2,
		Repo:     "depot/api",
		Sha:      "abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if len(recorder.requests) != 2 {
		t.Fatalf("expected 2 ListRuns requests, got %d", len(recorder.requests))
	}

	first := recorder.requests[0]
	if first.GetRepo() != "depot/api" {
		t.Fatalf("first Repo = %q, want depot/api", first.GetRepo())
	}
	if first.GetSha() != "abc123" {
		t.Fatalf("first Sha = %q, want abc123", first.GetSha())
	}
	if first.GetPageSize() != 2 {
		t.Fatalf("first PageSize = %d, want 2", first.GetPageSize())
	}
	if first.GetPageToken() != "" {
		t.Fatalf("first PageToken = %q, want empty", first.GetPageToken())
	}
	if got := first.GetStatus(); len(got) != 1 || got[0] != "failed" {
		t.Fatalf("first Status = %v, want [failed]", got)
	}

	second := recorder.requests[1]
	if second.GetRepo() != "depot/api" {
		t.Fatalf("second Repo = %q, want depot/api", second.GetRepo())
	}
	if second.GetSha() != "abc123" {
		t.Fatalf("second Sha = %q, want abc123", second.GetSha())
	}
	if second.GetPageSize() != 1 {
		t.Fatalf("second PageSize = %d, want 1", second.GetPageSize())
	}
	if second.GetPageToken() != "next" {
		t.Fatalf("second PageToken = %q, want next", second.GetPageToken())
	}
}
