package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

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

func (h ciServiceTestHandler) GetWorkflow(_ context.Context, req *connect.Request[civ1.GetWorkflowRequest]) (*connect.Response[civ1.GetWorkflowResponse], error) {
	assertAuthAndOrg(h.t, req.Header())
	if req.Msg.WorkflowId != "workflow-123" {
		h.t.Fatalf("WorkflowId = %q, want workflow-123", req.Msg.WorkflowId)
	}
	return connect.NewResponse(&civ1.GetWorkflowResponse{WorkflowId: req.Msg.WorkflowId, OrgId: "org-123"}), nil
}

func (h ciServiceTestHandler) GetJobAttemptLogs(context.Context, *connect.Request[civ1.GetJobAttemptLogsRequest]) (*connect.Response[civ1.GetJobAttemptLogsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) StreamJobAttemptLogs(context.Context, *connect.Request[civ1.StreamJobAttemptLogsRequest], *connect.ServerStream[civ1.StreamJobAttemptLogsResponse]) error {
	return connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) ExportJobAttemptLogs(context.Context, *connect.Request[civ1.ExportJobAttemptLogsRequest], *connect.ServerStream[civ1.ExportJobAttemptLogsResponse]) error {
	return connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) ListRuns(context.Context, *connect.Request[civ1.ListRunsRequest]) (*connect.Response[civ1.ListRunsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h ciServiceTestHandler) ListWorkflows(context.Context, *connect.Request[civ1.ListWorkflowsRequest]) (*connect.Response[civ1.ListWorkflowsResponse], error) {
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

func TestCIGetWorkflowWrapper(t *testing.T) {
	withTestCIService(t, func() {
		resp, err := CIGetWorkflow(context.Background(), "token-123", "org-123", "workflow-123")
		if err != nil {
			t.Fatalf("CIGetWorkflow returned error: %v", err)
		}
		if resp.WorkflowId != "workflow-123" || resp.OrgId != "org-123" {
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
		Statuses:    []string{"failed"},
		Limit:       2,
		Repo:        "depot/api",
		Sha:         "abc123",
		Trigger:     "workflow_dispatch",
		PullRequest: "42",
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
	if first.GetTrigger() != "workflow_dispatch" {
		t.Fatalf("first Trigger = %q, want workflow_dispatch", first.GetTrigger())
	}
	if first.GetPr() != "42" {
		t.Fatalf("first Pr = %q, want 42", first.GetPr())
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
	if second.GetTrigger() != "workflow_dispatch" {
		t.Fatalf("second Trigger = %q, want workflow_dispatch", second.GetTrigger())
	}
	if second.GetPr() != "42" {
		t.Fatalf("second Pr = %q, want 42", second.GetPr())
	}
	if second.GetPageSize() != 1 {
		t.Fatalf("second PageSize = %d, want 1", second.GetPageSize())
	}
	if second.GetPageToken() != "next" {
		t.Fatalf("second PageToken = %q, want next", second.GetPageToken())
	}
}

type listWorkflowsRecorder struct {
	civ1connect.UnimplementedCIServiceHandler
	requests []*civ1.ListWorkflowsRequest
	headers  []http.Header
}

func (r *listWorkflowsRecorder) ListWorkflows(ctx context.Context, req *connect.Request[civ1.ListWorkflowsRequest]) (*connect.Response[civ1.ListWorkflowsResponse], error) {
	r.requests = append(r.requests, proto.Clone(req.Msg).(*civ1.ListWorkflowsRequest))
	r.headers = append(r.headers, req.Header().Clone())

	resp := &civ1.ListWorkflowsResponse{
		Workflows: []*civ1.ListWorkflowsResponseWorkflow{
			{WorkflowId: "workflow-1"},
		},
	}
	return connect.NewResponse(resp), nil
}

func TestCIListWorkflowsSendsRecentDiscoveryFilters(t *testing.T) {
	recorder := &listWorkflowsRecorder{}
	_, handler := civ1connect.NewCIServiceHandler(recorder)
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	workflows, err := CIListWorkflows(context.Background(), "token-123", "org-123", CIListWorkflowsOptions{
		Limit:       2,
		Name:        "deploy",
		Repo:        "depot/api",
		Statuses:    []string{"running", "failed"},
		Trigger:     "workflow_dispatch",
		Sha:         "abc123",
		PullRequest: "42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(workflows))
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("expected 1 ListWorkflows request, got %d", len(recorder.requests))
	}

	if got := recorder.headers[0].Get("Authorization"); got != "Bearer token-123" {
		t.Fatalf("Authorization = %q, want Bearer token-123", got)
	}
	if got := recorder.headers[0].Get("x-depot-org"); got != "org-123" {
		t.Fatalf("x-depot-org = %q, want org-123", got)
	}

	request := recorder.requests[0]
	if request.GetPageSize() != 2 {
		t.Fatalf("PageSize = %d, want 2", request.GetPageSize())
	}
	if request.GetName() != "deploy" {
		t.Fatalf("Name = %q, want deploy", request.GetName())
	}
	if request.GetRepo() != "depot/api" {
		t.Fatalf("Repo = %q, want depot/api", request.GetRepo())
	}
	if got, want := request.GetStatus(), []string{"running", "failed"}; !slices.Equal(got, want) {
		t.Fatalf("Status = %v, want %v", got, want)
	}
	if request.GetTrigger() != "workflow_dispatch" {
		t.Fatalf("Trigger = %q, want workflow_dispatch", request.GetTrigger())
	}
	if request.GetSha() != "abc123" {
		t.Fatalf("Sha = %q, want abc123", request.GetSha())
	}
	if request.GetPr() != "42" {
		t.Fatalf("Pr = %q, want 42", request.GetPr())
	}
}

type streamLogsRecorder struct {
	civ1connect.UnimplementedCIServiceHandler
	requests []*civ1.StreamJobAttemptLogsRequest
}

func (r *streamLogsRecorder) StreamJobAttemptLogs(_ context.Context, req *connect.Request[civ1.StreamJobAttemptLogsRequest], stream *connect.ServerStream[civ1.StreamJobAttemptLogsResponse]) error {
	r.requests = append(r.requests, proto.Clone(req.Msg).(*civ1.StreamJobAttemptLogsRequest))

	if req.Msg.GetAttemptId() != "attempt-123" || req.Msg.GetJobId() != "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("unexpected stream target"))
	}

	switch len(r.requests) {
	case 1:
		if err := stream.Send(&civ1.StreamJobAttemptLogsResponse{
			AttemptStatus: "running",
		}); err != nil {
			return err
		}
		if err := stream.Send(&civ1.StreamJobAttemptLogsResponse{
			Line:          testLogLine("step-1", 1, "first"),
			NextCursor:    "cursor-1",
			AttemptStatus: "running",
		}); err != nil {
			return err
		}
		if err := stream.Send(&civ1.StreamJobAttemptLogsResponse{
			Line:          testLogLine("step-1", 2, "second"),
			NextCursor:    "cursor-2",
			AttemptStatus: "running",
		}); err != nil {
			return err
		}
		return connect.NewError(connect.CodeUnavailable, errors.New("stream interrupted"))
	case 2:
		if err := stream.Send(&civ1.StreamJobAttemptLogsResponse{
			Line:          testLogLine("step-1", 2, "second"),
			NextCursor:    "cursor-2-replay",
			AttemptStatus: "running",
		}); err != nil {
			return err
		}
		return connect.NewError(connect.CodeUnavailable, errors.New("stream interrupted after duplicate"))
	case 3:
		return stream.Send(&civ1.StreamJobAttemptLogsResponse{
			Line:          testLogLine("step-1", 3, "third"),
			NextCursor:    "cursor-3",
			AttemptStatus: "finished",
		})
	default:
		return nil
	}
}

func TestCIStreamJobAttemptLogsReconnectsFromLastWrittenCursorAndSuppressesDuplicates(t *testing.T) {
	recorder := &streamLogsRecorder{}
	_, handler := civ1connect.NewCIServiceHandler(recorder)
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	originalInitialBackoff := ciStreamInitialBackoff
	ciStreamInitialBackoff = 0
	t.Cleanup(func() { ciStreamInitialBackoff = originalInitialBackoff })

	var output bytes.Buffer
	var statuses []string
	if err := CIStreamJobAttemptLogs(context.Background(), "token-123", "org-123", CILogStreamTarget{AttemptID: "attempt-123"}, &output, func(status string) {
		statuses = append(statuses, status)
	}); err != nil {
		t.Fatal(err)
	}

	if got, want := output.String(), "first\nsecond\nthird\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if len(recorder.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(recorder.requests))
	}
	if got := recorder.requests[0].GetCursor(); got != "" {
		t.Fatalf("first cursor = %q, want empty", got)
	}
	if got := recorder.requests[1].GetCursor(); got != "cursor-2" {
		t.Fatalf("second cursor = %q, want cursor-2", got)
	}
	if got := recorder.requests[2].GetCursor(); got != "cursor-2" {
		t.Fatalf("third cursor = %q, want cursor-2", got)
	}
	if got, want := statuses, []string{"running", "running", "running", "running", "finished"}; !slices.Equal(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
}

func TestCIStreamJobAttemptLogLinesReturnsMetadataAndSuppressesDuplicates(t *testing.T) {
	recorder := &streamLogsRecorder{}
	_, handler := civ1connect.NewCIServiceHandler(recorder)
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	originalInitialBackoff := ciStreamInitialBackoff
	ciStreamInitialBackoff = 0
	t.Cleanup(func() { ciStreamInitialBackoff = originalInitialBackoff })

	var lines []*civ1.LogLine
	var statuses []string
	if err := CIStreamJobAttemptLogLines(context.Background(), "token-123", "org-123", CILogStreamTarget{AttemptID: "attempt-123"}, func(line *civ1.LogLine) error {
		lines = append(lines, proto.Clone(line).(*civ1.LogLine))
		return nil
	}, func(status string) error {
		statuses = append(statuses, status)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if got, want := len(lines), 3; got != want {
		t.Fatalf("lines = %d, want %d", got, want)
	}
	for i, wantBody := range []string{"first", "second", "third"} {
		if got := lines[i].GetBody(); got != wantBody {
			t.Fatalf("line %d body = %q, want %q", i, got, wantBody)
		}
		if got, want := lines[i].GetLineNumber(), uint32(i+1); got != want {
			t.Fatalf("line %d line number = %d, want %d", i, got, want)
		}
		if got, want := lines[i].GetTimestampMs(), int64(i+1); got != want {
			t.Fatalf("line %d timestamp_ms = %d, want %d", i, got, want)
		}
	}
	if got, want := statuses, []string{"running", "running", "running", "running", "finished"}; !slices.Equal(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
}

func TestCIStreamJobAttemptLogLinesPropagatesLineCallbackError(t *testing.T) {
	recorder := &streamLogsRecorder{}
	_, handler := civ1connect.NewCIServiceHandler(recorder)
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	originalInitialBackoff := ciStreamInitialBackoff
	ciStreamInitialBackoff = 0
	t.Cleanup(func() { ciStreamInitialBackoff = originalInitialBackoff })

	callbackErr := errors.New("callback failed")
	err := CIStreamJobAttemptLogLines(context.Background(), "token-123", "org-123", CILogStreamTarget{AttemptID: "attempt-123"}, func(*civ1.LogLine) error {
		return callbackErr
	}, nil)
	if !errors.Is(err, callbackErr) {
		t.Fatalf("expected callback error, got %v", err)
	}
}

type exportLogsRecorder struct {
	civ1connect.UnimplementedCIServiceHandler
	requests []*civ1.ExportJobAttemptLogsRequest
}

func (r *exportLogsRecorder) ExportJobAttemptLogs(_ context.Context, req *connect.Request[civ1.ExportJobAttemptLogsRequest], stream *connect.ServerStream[civ1.ExportJobAttemptLogsResponse]) error {
	r.requests = append(r.requests, proto.Clone(req.Msg).(*civ1.ExportJobAttemptLogsRequest))
	if req.Msg.GetJobId() != "job-123" || req.Msg.GetAttemptId() != "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("unexpected export target"))
	}
	if req.Msg.GetFormat() != civ1.JobAttemptLogExportFormat_JOB_ATTEMPT_LOG_EXPORT_FORMAT_JSONL {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("unexpected export format"))
	}

	if err := stream.Send(&civ1.ExportJobAttemptLogsResponse{
		Event: &civ1.ExportJobAttemptLogsResponse_Metadata{
			Metadata: &civ1.JobAttemptLogExportMetadata{
				Filename:    "logs.jsonl",
				ContentType: "application/x-ndjson; charset=utf-8",
				Format:      civ1.JobAttemptLogExportFormat_JOB_ATTEMPT_LOG_EXPORT_FORMAT_JSONL,
			},
		},
	}); err != nil {
		return err
	}
	if err := stream.Send(&civ1.ExportJobAttemptLogsResponse{
		Event: &civ1.ExportJobAttemptLogsResponse_Chunk{Chunk: []byte("first\n")},
	}); err != nil {
		return err
	}
	return stream.Send(&civ1.ExportJobAttemptLogsResponse{
		Event: &civ1.ExportJobAttemptLogsResponse_Chunk{Chunk: []byte("second\n")},
	})
}

func TestCIExportJobAttemptLogsStreamsChunks(t *testing.T) {
	recorder := &exportLogsRecorder{}
	_, handler := civ1connect.NewCIServiceHandler(recorder)
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	var output bytes.Buffer
	metadata, err := CIExportJobAttemptLogs(
		context.Background(),
		"token-123",
		"org-123",
		CILogStreamTarget{JobID: "job-123"},
		civ1.JobAttemptLogExportFormat_JOB_ATTEMPT_LOG_EXPORT_FORMAT_JSONL,
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "first\nsecond\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if metadata.GetContentType() != "application/x-ndjson; charset=utf-8" {
		t.Fatalf("content type = %q", metadata.GetContentType())
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(recorder.requests))
	}
}

type exportChunkBeforeMetadataRecorder struct {
	civ1connect.UnimplementedCIServiceHandler
}

func (r exportChunkBeforeMetadataRecorder) ExportJobAttemptLogs(_ context.Context, _ *connect.Request[civ1.ExportJobAttemptLogsRequest], stream *connect.ServerStream[civ1.ExportJobAttemptLogsResponse]) error {
	return stream.Send(&civ1.ExportJobAttemptLogsResponse{
		Event: &civ1.ExportJobAttemptLogsResponse_Chunk{Chunk: []byte("body\n")},
	})
}

func TestCIExportJobAttemptLogsRejectsChunkBeforeMetadata(t *testing.T) {
	_, handler := civ1connect.NewCIServiceHandler(exportChunkBeforeMetadataRecorder{})
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	var output bytes.Buffer
	_, err := CIExportJobAttemptLogs(
		context.Background(),
		"token-123",
		"org-123",
		CILogStreamTarget{AttemptID: "attempt-123"},
		civ1.JobAttemptLogExportFormat_JOB_ATTEMPT_LOG_EXPORT_FORMAT_TEXT,
		&output,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "chunk before metadata") {
		t.Fatalf("unexpected error: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want empty", output.String())
	}
}

type statusOnlyStreamRecorder struct {
	civ1connect.UnimplementedCIServiceHandler
	requests []*civ1.StreamJobAttemptLogsRequest
}

func (r *statusOnlyStreamRecorder) StreamJobAttemptLogs(_ context.Context, req *connect.Request[civ1.StreamJobAttemptLogsRequest], stream *connect.ServerStream[civ1.StreamJobAttemptLogsResponse]) error {
	r.requests = append(r.requests, proto.Clone(req.Msg).(*civ1.StreamJobAttemptLogsRequest))

	switch len(r.requests) {
	case 1:
		return connect.NewError(connect.CodeUnavailable, errors.New("stream unavailable"))
	case 2:
		if err := stream.Send(&civ1.StreamJobAttemptLogsResponse{AttemptStatus: "running"}); err != nil {
			return err
		}
		return connect.NewError(connect.CodeUnavailable, errors.New("status-only stream interrupted"))
	default:
		return nil
	}
}

func TestCIStreamJobAttemptLogsResetsBackoffAfterStatusOnlyMessage(t *testing.T) {
	recorder := &statusOnlyStreamRecorder{}
	_, handler := civ1connect.NewCIServiceHandler(recorder)
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	originalInitialBackoff := ciStreamInitialBackoff
	ciStreamInitialBackoff = 10 * time.Millisecond
	t.Cleanup(func() { ciStreamInitialBackoff = originalInitialBackoff })

	originalSleep := ciStreamSleep
	var sleeps []time.Duration
	ciStreamSleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}
	t.Cleanup(func() { ciStreamSleep = originalSleep })

	var statuses []string
	if err := CIStreamJobAttemptLogs(context.Background(), "token-123", "org-123", CILogStreamTarget{AttemptID: "attempt-123"}, io.Discard, func(status string) {
		statuses = append(statuses, status)
	}); err != nil {
		t.Fatal(err)
	}

	if len(recorder.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(recorder.requests))
	}
	if got, want := sleeps, []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}; !slices.Equal(got, want) {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
	if got, want := statuses, []string{"running"}; !slices.Equal(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
}

func TestCIStreamJobAttemptLogsSendsJobIDTarget(t *testing.T) {
	recorder := &streamLogsRecorder{}
	_, handler := civ1connect.NewCIServiceHandler(recorder)
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	if err := CIStreamJobAttemptLogs(context.Background(), "token-123", "org-123", CILogStreamTarget{JobID: "job-123"}, io.Discard, nil); err == nil {
		t.Fatal("expected test handler to reject the job ID target after recording it")
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(recorder.requests))
	}
	if got := recorder.requests[0].GetJobId(); got != "job-123" {
		t.Fatalf("job ID = %q, want job-123", got)
	}
	if got := recorder.requests[0].GetAttemptId(); got != "" {
		t.Fatalf("attempt ID = %q, want empty", got)
	}
}

func TestIsTransientConnectError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "context canceled",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "connect-wrapped context deadline exceeded",
			err:  connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded),
			want: false,
		},
		{
			name: "connect unavailable",
			err:  connect.NewError(connect.CodeUnavailable, errors.New("stream interrupted")),
			want: true,
		},
		{
			name: "connect deadline exceeded",
			err:  connect.NewError(connect.CodeDeadlineExceeded, errors.New("server deadline exceeded")),
			want: true,
		},
		{
			name: "connect invalid argument",
			err:  connect.NewError(connect.CodeInvalidArgument, errors.New("bad request")),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientConnectError(tt.err); got != tt.want {
				t.Fatalf("isTransientConnectError(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

func testLogLine(stepID string, lineNumber uint32, body string) *civ1.LogLine {
	return &civ1.LogLine{
		StepKey:     stepID,
		TimestampMs: int64(lineNumber),
		LineNumber:  lineNumber,
		Stream:      0,
		Body:        body,
	}
}
