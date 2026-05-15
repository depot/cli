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
	civ3beta2 "github.com/depot/cli/pkg/proto/depot/ci/v3beta2"
	"github.com/depot/cli/pkg/proto/depot/ci/v3beta2/civ3beta2connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
)

type ciServiceTestHandler struct {
	civ1connect.UnimplementedCIServiceHandler
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

func (h ciServiceTestHandler) GetFailureDiagnosis(_ context.Context, req *connect.Request[civ1.GetFailureDiagnosisRequest]) (*connect.Response[civ1.GetFailureDiagnosisResponse], error) {
	assertAuthAndOrg(h.t, req.Header())
	if req.Msg.TargetId != "job-123" {
		h.t.Fatalf("TargetId = %q, want job-123", req.Msg.TargetId)
	}
	if req.Msg.TargetType != civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_JOB {
		h.t.Fatalf("TargetType = %v, want job", req.Msg.TargetType)
	}
	return connect.NewResponse(&civ1.GetFailureDiagnosisResponse{
		OrgId: "org-123",
		Target: &civ1.FailureDiagnosisTarget{
			TargetId:   req.Msg.TargetId,
			TargetType: req.Msg.TargetType,
			Status:     civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
		},
		State: civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_FOCUSED_FAILURE,
	}), nil
}

func (h ciServiceTestHandler) GetWorkflow(_ context.Context, req *connect.Request[civ1.GetWorkflowRequest]) (*connect.Response[civ1.GetWorkflowResponse], error) {
	assertAuthAndOrg(h.t, req.Header())
	if req.Msg.WorkflowId != "workflow-123" {
		h.t.Fatalf("WorkflowId = %q, want workflow-123", req.Msg.WorkflowId)
	}
	return connect.NewResponse(&civ1.GetWorkflowResponse{WorkflowId: req.Msg.WorkflowId, OrgId: "org-123"}), nil
}

func (h ciServiceTestHandler) GetJobAttemptMetrics(_ context.Context, req *connect.Request[civ1.GetJobAttemptMetricsRequest]) (*connect.Response[civ1.GetJobAttemptMetricsResponse], error) {
	assertAuthAndOrg(h.t, req.Header())
	if req.Msg.AttemptId != "attempt-123" {
		h.t.Fatalf("AttemptId = %q, want attempt-123", req.Msg.AttemptId)
	}
	return connect.NewResponse(&civ1.GetJobAttemptMetricsResponse{SnapshotAt: "2026-05-03T12:00:00Z"}), nil
}

func (h ciServiceTestHandler) GetJobMetrics(_ context.Context, req *connect.Request[civ1.GetJobMetricsRequest]) (*connect.Response[civ1.GetJobMetricsResponse], error) {
	assertAuthAndOrg(h.t, req.Header())
	if req.Msg.JobId != "job-123" {
		h.t.Fatalf("JobId = %q, want job-123", req.Msg.JobId)
	}
	return connect.NewResponse(&civ1.GetJobMetricsResponse{SnapshotAt: "2026-05-03T12:00:00Z"}), nil
}

func (h ciServiceTestHandler) GetRunMetrics(_ context.Context, req *connect.Request[civ1.GetRunMetricsRequest]) (*connect.Response[civ1.GetRunMetricsResponse], error) {
	assertAuthAndOrg(h.t, req.Header())
	if req.Msg.RunId != "run-123" {
		h.t.Fatalf("RunId = %q, want run-123", req.Msg.RunId)
	}
	return connect.NewResponse(&civ1.GetRunMetricsResponse{SnapshotAt: "2026-05-03T12:00:00Z"}), nil
}

func (h ciServiceTestHandler) GetJobSummary(_ context.Context, req *connect.Request[civ1.GetJobSummaryRequest]) (*connect.Response[civ1.GetJobSummaryResponse], error) {
	assertAuthAndOrg(h.t, req.Header())
	if req.Msg.AttemptId != "" {
		if req.Msg.AttemptId != "attempt-123" {
			h.t.Fatalf("AttemptId = %q, want attempt-123", req.Msg.AttemptId)
		}
		return connect.NewResponse(&civ1.GetJobSummaryResponse{AttemptId: req.Msg.AttemptId, HasSummary: true, Markdown: "attempt summary"}), nil
	}
	if req.Msg.JobId != "job-123" {
		h.t.Fatalf("JobId = %q, want job-123", req.Msg.JobId)
	}
	return connect.NewResponse(&civ1.GetJobSummaryResponse{JobId: req.Msg.JobId, AttemptId: "attempt-456", HasSummary: true, Markdown: "job summary"}), nil
}

func (h ciServiceTestHandler) ListArtifacts(_ context.Context, req *connect.Request[civ1.ListArtifactsRequest]) (*connect.Response[civ1.ListArtifactsResponse], error) {
	assertAuthAndOrg(h.t, req.Header())
	if req.Msg.GetRunId() == "run-artifacts-no-filters" {
		if req.Msg.WorkflowId != nil || req.Msg.JobId != nil || req.Msg.AttemptId != nil {
			h.t.Fatalf("artifact filters should be absent when not provided: %+v", req.Msg)
		}
		return connect.NewResponse(&civ1.ListArtifactsResponse{}), nil
	}
	if req.Msg.GetRunId() != "run-artifacts" {
		h.t.Fatalf("RunId = %q, want run-artifacts", req.Msg.GetRunId())
	}
	if req.Msg.GetWorkflowId() != "workflow-123" {
		h.t.Fatalf("WorkflowId = %q, want workflow-123", req.Msg.GetWorkflowId())
	}
	if req.Msg.GetJobId() != "job-123" {
		h.t.Fatalf("JobId = %q, want job-123", req.Msg.GetJobId())
	}
	if req.Msg.GetAttemptId() != "attempt-123" {
		h.t.Fatalf("AttemptId = %q, want attempt-123", req.Msg.GetAttemptId())
	}
	if req.Msg.GetPageSize() != 500 {
		h.t.Fatalf("PageSize = %d, want 500", req.Msg.GetPageSize())
	}

	switch req.Msg.GetPageToken() {
	case "":
		nextPageToken := "next"
		return connect.NewResponse(&civ1.ListArtifactsResponse{
			Artifacts: []*civ1.Artifact{
				{ArtifactId: "artifact-1", Name: "first.txt", SizeBytes: 10},
			},
			NextPageToken: &nextPageToken,
		}), nil
	case "next":
		return connect.NewResponse(&civ1.ListArtifactsResponse{
			Artifacts: []*civ1.Artifact{
				{ArtifactId: "artifact-2", Name: "second.txt", SizeBytes: 20},
			},
		}), nil
	default:
		h.t.Fatalf("PageToken = %q, want empty or next", req.Msg.GetPageToken())
		return nil, nil
	}
}

func (h ciServiceTestHandler) GetArtifactDownloadURL(_ context.Context, req *connect.Request[civ1.GetArtifactDownloadURLRequest]) (*connect.Response[civ1.GetArtifactDownloadURLResponse], error) {
	assertAuthAndOrg(h.t, req.Header())
	if req.Msg.GetArtifactId() != "artifact-123" {
		h.t.Fatalf("ArtifactId = %q, want artifact-123", req.Msg.GetArtifactId())
	}
	return connect.NewResponse(&civ1.GetArtifactDownloadURLResponse{
		Artifact: &civ1.Artifact{ArtifactId: req.Msg.GetArtifactId(), Name: "artifact.txt", SizeBytes: 12},
		Url:      "https://example.test/artifact.txt",
	}), nil
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

func TestCIGetFailureDiagnosisWrapper(t *testing.T) {
	withTestCIService(t, func() {
		resp, err := CIGetFailureDiagnosis(
			context.Background(),
			"token-123",
			"org-123",
			&civ1.GetFailureDiagnosisRequest{
				TargetId:   "job-123",
				TargetType: civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_JOB,
			},
		)
		if err != nil {
			t.Fatalf("CIGetFailureDiagnosis returned error: %v", err)
		}
		if resp.GetTarget().GetTargetId() != "job-123" || resp.GetState() != civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_FOCUSED_FAILURE {
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

func TestCIMetricsWrappers(t *testing.T) {
	withTestCIService(t, func() {
		attemptResp, err := CIGetJobAttemptMetrics(context.Background(), "token-123", "org-123", "attempt-123")
		if err != nil {
			t.Fatalf("CIGetJobAttemptMetrics returned error: %v", err)
		}
		if attemptResp.SnapshotAt == "" {
			t.Fatal("CIGetJobAttemptMetrics returned empty snapshot")
		}

		jobResp, err := CIGetJobMetrics(context.Background(), "token-123", "org-123", "job-123")
		if err != nil {
			t.Fatalf("CIGetJobMetrics returned error: %v", err)
		}
		if jobResp.SnapshotAt == "" {
			t.Fatal("CIGetJobMetrics returned empty snapshot")
		}

		runResp, err := CIGetRunMetrics(context.Background(), "token-123", "org-123", "run-123")
		if err != nil {
			t.Fatalf("CIGetRunMetrics returned error: %v", err)
		}
		if runResp.SnapshotAt == "" {
			t.Fatal("CIGetRunMetrics returned empty snapshot")
		}
	})
}

func TestCISummaryWrappers(t *testing.T) {
	withTestCIService(t, func() {
		attemptResp, err := CIGetJobSummary(
			context.Background(),
			"token-123",
			"org-123",
			&civ1.GetJobSummaryRequest{AttemptId: "attempt-123"},
		)
		if err != nil {
			t.Fatalf("CIGetJobSummary attempt request returned error: %v", err)
		}
		if attemptResp.GetMarkdown() != "attempt summary" {
			t.Fatalf("unexpected attempt summary: %+v", attemptResp)
		}

		jobResp, err := CIGetJobSummary(
			context.Background(),
			"token-123",
			"org-123",
			&civ1.GetJobSummaryRequest{JobId: "job-123"},
		)
		if err != nil {
			t.Fatalf("CIGetJobSummary job request returned error: %v", err)
		}
		if jobResp.GetAttemptId() != "attempt-456" || jobResp.GetMarkdown() != "job summary" {
			t.Fatalf("unexpected job summary: %+v", jobResp)
		}
	})
}

func TestCIArtifactsWrappers(t *testing.T) {
	withTestCIService(t, func() {
		artifacts, err := CIListArtifacts(context.Background(), "token-123", "org-123", "run-artifacts", CIListArtifactsOptions{
			WorkflowID: "workflow-123",
			JobID:      "job-123",
			AttemptID:  "attempt-123",
		})
		if err != nil {
			t.Fatalf("CIListArtifacts returned error: %v", err)
		}
		if len(artifacts) != 2 {
			t.Fatalf("len(artifacts) = %d, want 2", len(artifacts))
		}
		if artifacts[0].GetArtifactId() != "artifact-1" || artifacts[1].GetArtifactId() != "artifact-2" {
			t.Fatalf("unexpected artifacts: %+v", artifacts)
		}

		_, err = CIListArtifacts(context.Background(), "token-123", "org-123", "run-artifacts-no-filters", CIListArtifactsOptions{})
		if err != nil {
			t.Fatalf("CIListArtifacts without filters returned error: %v", err)
		}

		resp, err := CIGetArtifactDownloadURL(context.Background(), "token-123", "org-123", "artifact-123")
		if err != nil {
			t.Fatalf("CIGetArtifactDownloadURL returned error: %v", err)
		}
		if resp.GetUrl() != "https://example.test/artifact.txt" {
			t.Fatalf("Url = %q, want signed URL", resp.GetUrl())
		}
		if resp.GetArtifact().GetArtifactId() != "artifact-123" {
			t.Fatalf("ArtifactId = %q, want artifact-123", resp.GetArtifact().GetArtifactId())
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

type secretVariantsRecorder struct {
	civ3beta2connect.UnimplementedSecretServiceHandler
	t            *testing.T
	getRequests  []*civ3beta2.GetSecretVariantRequest
	setRequests  []*civ3beta2.SetSecretVariantRequest
	listRequests []*civ3beta2.ListSecretsRequest
}

func (r *secretVariantsRecorder) GetSecretVariant(_ context.Context, req *connect.Request[civ3beta2.GetSecretVariantRequest]) (*connect.Response[civ3beta2.GetSecretVariantResponse], error) {
	assertAuthAndOrg(r.t, req.Header())
	r.getRequests = append(r.getRequests, proto.Clone(req.Msg).(*civ3beta2.GetSecretVariantRequest))
	return connect.NewResponse(&civ3beta2.GetSecretVariantResponse{
		Variant: &civ3beta2.SecretVariant{
			Id:       req.Msg.GetId(),
			SecretId: "secret-1",
			Name:     "production",
			Attributes: []*civ3beta2.Attribute{
				{Key: "repository", Value: "depot/api"},
				{Key: "branch", Value: "main"},
			},
		},
	}), nil
}

func (r *secretVariantsRecorder) SetSecretVariant(_ context.Context, req *connect.Request[civ3beta2.SetSecretVariantRequest]) (*connect.Response[civ3beta2.SetSecretVariantResponse], error) {
	assertAuthAndOrg(r.t, req.Header())
	r.setRequests = append(r.setRequests, proto.Clone(req.Msg).(*civ3beta2.SetSecretVariantRequest))
	return connect.NewResponse(&civ3beta2.SetSecretVariantResponse{
		Secret: &civ3beta2.Secret{
			Id:   "secret-1",
			Name: req.Msg.GetSecretName(),
		},
		Variant: &civ3beta2.SecretVariant{
			Id:          "variant-1",
			SecretId:    "secret-1",
			Name:        req.Msg.GetVariantName(),
			Description: req.Msg.Description,
			Attributes:  req.Msg.GetAttributes(),
		},
		CreatedSecret:  true,
		CreatedVariant: true,
	}), nil
}

func (r *secretVariantsRecorder) ListSecrets(_ context.Context, req *connect.Request[civ3beta2.ListSecretsRequest]) (*connect.Response[civ3beta2.ListSecretsResponse], error) {
	assertAuthAndOrg(r.t, req.Header())
	r.listRequests = append(r.listRequests, proto.Clone(req.Msg).(*civ3beta2.ListSecretsRequest))
	valueGroupIndex := uint32(2)
	name := "TOKEN"
	if req.Msg.GetPage().GetPage() > 1 {
		name = "TOKEN_TWO"
	}
	return connect.NewResponse(&civ3beta2.ListSecretsResponse{
		Secrets: []*civ3beta2.Secret{
			{
				Id:           "secret-1",
				Name:         name,
				VariantCount: 1,
				Variants: []*civ3beta2.SecretVariant{
					{
						Id:              "variant-1",
						SecretId:        "secret-1",
						Name:            "default",
						Attributes:      []*civ3beta2.Attribute{{Key: "repository", Value: "depot/api"}},
						ValueGroupIndex: &valueGroupIndex,
					},
				},
			},
		},
		Page: &civ3beta2.PageResponse{Page: req.Msg.GetPage().GetPage(), PageSize: 100, HasMore: req.Msg.GetPage().GetPage() == 1},
	}), nil
}

func TestCISetSecretVariantMapsDefaultVariantAndAttributes(t *testing.T) {
	recorder := &secretVariantsRecorder{t: t}
	withTestSecretVariantsService(t, recorder, func() {
		result, err := CISetSecretVariant(context.Background(), "token-123", "org-123", CISetSecretVariantOptions{
			Name:        "TOKEN",
			Value:       "secret-value",
			Description: "deploy token",
			Repo:        []string{"depot/api"},
			Environment: []string{"production"},
			Branch:      []string{"main", "release/*"},
			Workflow:    []string{"deploy.yml"},
		})
		if err != nil {
			t.Fatalf("CISetSecretVariant returned error: %v", err)
		}
		if !result.CreatedSecret || !result.CreatedVariant {
			t.Fatalf("expected created flags, got %+v", result)
		}
		if result.Variant.Name != "default" {
			t.Fatalf("variant name = %q, want default", result.Variant.Name)
		}
	})

	if len(recorder.setRequests) != 1 {
		t.Fatalf("expected 1 SetSecretVariant request, got %d", len(recorder.setRequests))
	}
	req := recorder.setRequests[0]
	if req.GetSecretName() != "TOKEN" || req.GetVariantName() != "default" || req.GetValue() != "secret-value" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.GetDescription() != "deploy token" {
		t.Fatalf("description = %q, want deploy token", req.GetDescription())
	}
	assertProtoAttributes(t, req.GetAttributes(), []CIVariantAttribute{
		{Key: "repository", Value: "depot/api"},
		{Key: "environment", Value: "production"},
		{Key: "branch", Value: "main"},
		{Key: "branch", Value: "release/*"},
		{Key: "workflow", Value: "deploy.yml"},
	})
}

func TestCIGetSecretVariantByID(t *testing.T) {
	recorder := &secretVariantsRecorder{t: t}
	withTestSecretVariantsService(t, recorder, func() {
		variant, err := CIGetSecretVariant(context.Background(), "token-123", "org-123", "variant-123")
		if err != nil {
			t.Fatalf("CIGetSecretVariant returned error: %v", err)
		}
		if variant.ID != "variant-123" || variant.Name != "production" || len(variant.Attributes) != 2 {
			t.Fatalf("unexpected variant: %+v", variant)
		}
	})

	if len(recorder.getRequests) != 1 {
		t.Fatalf("expected 1 GetSecretVariant request, got %d", len(recorder.getRequests))
	}
	if got := recorder.getRequests[0].GetId(); got != "variant-123" {
		t.Fatalf("id = %q, want variant-123", got)
	}
}

func TestCIListSecretVariantsFetchesAllPagesAndMapsResponse(t *testing.T) {
	recorder := &secretVariantsRecorder{t: t}
	withTestSecretVariantsService(t, recorder, func() {
		result, err := CIListSecretVariants(context.Background(), "token-123", "org-123", CIListSecretVariantsOptions{
			Repo: []string{"depot/api"},
		})
		if err != nil {
			t.Fatalf("CIListSecretVariants returned error: %v", err)
		}
		if len(result.Secrets) != 2 || result.Secrets[0].Name != "TOKEN" || result.Secrets[1].Name != "TOKEN_TWO" {
			t.Fatalf("unexpected secrets: %+v", result.Secrets)
		}
		if result.Secrets[0].Variants[0].ValueGroupIndex == nil || *result.Secrets[0].Variants[0].ValueGroupIndex != 2 {
			t.Fatalf("value group index = %v, want 2", result.Secrets[0].Variants[0].ValueGroupIndex)
		}
	})

	if len(recorder.listRequests) != 2 {
		t.Fatalf("expected 2 ListSecrets requests, got %d", len(recorder.listRequests))
	}
	req := recorder.listRequests[0]
	if req.GetPage().GetPage() != 1 || req.GetPage().GetPageSize() != 100 {
		t.Fatalf("page request = %+v, want page=1 page_size=100", req.GetPage())
	}
	if got := recorder.listRequests[1].GetPage().GetPage(); got != 2 {
		t.Fatalf("second page = %d, want 2", got)
	}
	assertProtoAttributes(t, req.GetAttributes(), []CIVariantAttribute{{Key: "repository", Value: "depot/api"}})
}

func withTestSecretVariantsService(t *testing.T, handler civ3beta2connect.SecretServiceHandler, fn func()) {
	t.Helper()

	_, httpHandler := civ3beta2connect.NewSecretServiceHandler(handler)
	server := httptest.NewServer(h2c.NewHandler(httpHandler, &http2.Server{}))
	defer server.Close()

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	fn()
}

type variableVariantsRecorder struct {
	civ3beta2connect.UnimplementedVariableServiceHandler
	t            *testing.T
	setRequests  []*civ3beta2.SetVariableVariantRequest
	listRequests []*civ3beta2.ListVariablesRequest
}

func (r *variableVariantsRecorder) SetVariableVariant(_ context.Context, req *connect.Request[civ3beta2.SetVariableVariantRequest]) (*connect.Response[civ3beta2.SetVariableVariantResponse], error) {
	assertAuthAndOrg(r.t, req.Header())
	r.setRequests = append(r.setRequests, proto.Clone(req.Msg).(*civ3beta2.SetVariableVariantRequest))
	return connect.NewResponse(&civ3beta2.SetVariableVariantResponse{
		Variable: &civ3beta2.Variable{
			Id:   "variable-1",
			Name: req.Msg.GetVariableName(),
		},
		Variant: &civ3beta2.VariableVariant{
			Id:         "variant-1",
			VariableId: "variable-1",
			Name:       req.Msg.GetVariantName(),
			Value:      req.Msg.GetValue(),
			Attributes: req.Msg.GetAttributes(),
		},
		CreatedVariable: true,
		CreatedVariant:  true,
	}), nil
}

func (r *variableVariantsRecorder) ListVariables(_ context.Context, req *connect.Request[civ3beta2.ListVariablesRequest]) (*connect.Response[civ3beta2.ListVariablesResponse], error) {
	assertAuthAndOrg(r.t, req.Header())
	r.listRequests = append(r.listRequests, proto.Clone(req.Msg).(*civ3beta2.ListVariablesRequest))
	name := "REGION"
	if req.Msg.GetPage().GetPage() > 1 {
		name = "ENV"
	}
	return connect.NewResponse(&civ3beta2.ListVariablesResponse{
		Variables: []*civ3beta2.Variable{
			{
				Id:           "variable-1",
				Name:         name,
				VariantCount: 1,
				Variants: []*civ3beta2.VariableVariant{
					{
						Id:         "variant-1",
						VariableId: "variable-1",
						Name:       "default",
						Value:      "production",
						Attributes: []*civ3beta2.Attribute{{Key: "repository", Value: "depot/api"}},
					},
				},
			},
		},
		Page: &civ3beta2.PageResponse{Page: req.Msg.GetPage().GetPage(), PageSize: 100, HasMore: req.Msg.GetPage().GetPage() == 1},
	}), nil
}

func TestCISetVariableVariantMapsNamedVariantAndAttributes(t *testing.T) {
	recorder := &variableVariantsRecorder{t: t}
	withTestVariableVariantsService(t, recorder, func() {
		result, err := CISetVariableVariant(context.Background(), "token-123", "org-123", CISetVariableVariantOptions{
			Name:    "REGION",
			Variant: "production",
			Value:   "us-east-1",
			Repo:    []string{"depot/api"},
		})
		if err != nil {
			t.Fatalf("CISetVariableVariant returned error: %v", err)
		}
		if result.Variant.Name != "production" || result.Variant.Value != "us-east-1" {
			t.Fatalf("unexpected variant: %+v", result.Variant)
		}
	})

	if len(recorder.setRequests) != 1 {
		t.Fatalf("expected 1 SetVariableVariant request, got %d", len(recorder.setRequests))
	}
	req := recorder.setRequests[0]
	if req.GetVariableName() != "REGION" || req.GetVariantName() != "production" || req.GetValue() != "us-east-1" {
		t.Fatalf("unexpected request: %+v", req)
	}
	assertProtoAttributes(t, req.GetAttributes(), []CIVariantAttribute{{Key: "repository", Value: "depot/api"}})
}

func TestCIListVariableVariantsFetchesAllPagesAndMapsResponse(t *testing.T) {
	recorder := &variableVariantsRecorder{t: t}
	withTestVariableVariantsService(t, recorder, func() {
		result, err := CIListVariableVariants(context.Background(), "token-123", "org-123", CIListVariableVariantsOptions{
			Repo: []string{"depot/api"},
		})
		if err != nil {
			t.Fatalf("CIListVariableVariants returned error: %v", err)
		}
		if len(result.Variables) != 2 || result.Variables[0].Name != "REGION" || result.Variables[1].Name != "ENV" {
			t.Fatalf("unexpected variables: %+v", result.Variables)
		}
	})

	if len(recorder.listRequests) != 2 {
		t.Fatalf("expected 2 ListVariables requests, got %d", len(recorder.listRequests))
	}
	first := recorder.listRequests[0]
	if first.GetPage().GetPage() != 1 || first.GetPage().GetPageSize() != 100 {
		t.Fatalf("first page request = %+v, want page=1 page_size=100", first.GetPage())
	}
	if got := recorder.listRequests[1].GetPage().GetPage(); got != 2 {
		t.Fatalf("second page = %d, want 2", got)
	}
	assertProtoAttributes(t, first.GetAttributes(), []CIVariantAttribute{{Key: "repository", Value: "depot/api"}})
}

func withTestVariableVariantsService(t *testing.T, handler civ3beta2connect.VariableServiceHandler, fn func()) {
	t.Helper()

	_, httpHandler := civ3beta2connect.NewVariableServiceHandler(handler)
	server := httptest.NewServer(h2c.NewHandler(httpHandler, &http2.Server{}))
	defer server.Close()

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	fn()
}

func assertProtoAttributes(t *testing.T, got []*civ3beta2.Attribute, want []CIVariantAttribute) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("attributes length = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].GetKey() != want[i].Key || got[i].GetValue() != want[i].Value {
			t.Fatalf("attribute %d = %s=%s, want %s=%s", i, got[i].GetKey(), got[i].GetValue(), want[i].Key, want[i].Value)
		}
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
