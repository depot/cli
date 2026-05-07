package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func TestSummaryAttemptPrintsMarkdownOnlyToStdout(t *testing.T) {
	restoreSummaryAPIs(t)

	var capturedAttemptID string
	ciGetJobSummary = func(ctx context.Context, token, orgID string, req *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
		if req.GetJobId() != "" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("job not found"))
		}
		capturedAttemptID = req.GetAttemptId()
		return summaryResponse("attempt-1", "job-1", "## Build\n\nok"), nil
	}

	stdout, stderr, err := executeSummaryCommand("attempt-1")
	if err != nil {
		t.Fatal(err)
	}
	if capturedAttemptID != "attempt-1" {
		t.Fatalf("attemptID = %q, want attempt-1", capturedAttemptID)
	}
	if stdout != "## Build\n\nok\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestSummaryAttemptJSONOutput(t *testing.T) {
	restoreSummaryAPIs(t)

	ciGetJobSummary = func(ctx context.Context, token, orgID string, req *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
		if req.GetJobId() != "" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("job not found"))
		}
		return summaryResponse(req.GetAttemptId(), "job-1", "## Build\n\nok"), nil
	}

	cmd := NewCmdSummary()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output", "json", "attempt-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}

	var got summaryJSONDocument
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	if got.AttemptID != "attempt-1" || got.JobID != "job-1" || !got.HasSummary || got.Markdown != "## Build\n\nok" {
		t.Fatalf("unexpected JSON document: %+v", got)
	}
	if got.StepCount != 1 {
		t.Fatalf("step_count = %d, want 1", got.StepCount)
	}
}

func TestSummaryResolvesJobBeforeAttempt(t *testing.T) {
	restoreSummaryAPIs(t)

	var requests []string
	ciGetJobSummary = func(ctx context.Context, token, orgID string, req *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
		requests = append(requests, "job:"+req.GetJobId()+" attempt:"+req.GetAttemptId())
		if req.GetJobId() != "job-1" {
			t.Fatalf("JobId = %q, want job-1", req.GetJobId())
		}
		return summaryResponse("attempt-3", "job-1", "job markdown"), nil
	}

	stdout, stderr, err := executeSummaryCommand("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "job markdown\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if strings.Join(requests, ",") != "job:job-1 attempt:" {
		t.Fatalf("requests = %#v, want job lookup only", requests)
	}
	if !strings.Contains(stderr, "Using attempt #3 attempt-3 for job job-1.") {
		t.Fatalf("stderr missing resolution note: %q", stderr)
	}
}

func TestSummaryEmptyAttemptIsNonError(t *testing.T) {
	restoreSummaryAPIs(t)

	ciGetJobSummary = func(ctx context.Context, token, orgID string, req *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
		if req.GetJobId() != "" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("job not found"))
		}
		return &civ1.GetJobSummaryResponse{
			AttemptId:     req.GetAttemptId(),
			JobId:         "job-1",
			HasSummary:    false,
			EmptyReason:   "no_summary",
			Attempt:       1,
			JobStatus:     "finished",
			AttemptStatus: "finished",
		}, nil
	}
	stdout, stderr, err := executeSummaryCommand("attempt-1")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "No CI step summary was produced for this attempt.\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestSummaryNoAttemptJobIsNonError(t *testing.T) {
	restoreSummaryAPIs(t)

	ciGetJobSummary = func(ctx context.Context, token, orgID string, req *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
		if req.GetAttemptId() != "" {
			t.Fatal("attempt lookup should not be called for a resolved job")
			return nil, nil
		}
		return &civ1.GetJobSummaryResponse{
			JobId:       req.GetJobId(),
			JobStatus:   "queued",
			HasSummary:  false,
			EmptyReason: "no_attempt",
		}, nil
	}

	stdout, stderr, err := executeSummaryCommand("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "No CI job attempts exist yet, so no step summary is available.\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestSummaryJSONJobFallbackEmptyIncludesResolvedAttempt(t *testing.T) {
	restoreSummaryAPIs(t)

	ciGetJobSummary = func(ctx context.Context, token, orgID string, req *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
		if req.GetAttemptId() != "" {
			t.Fatal("attempt lookup should not be called for a resolved job")
			return nil, nil
		}
		return &civ1.GetJobSummaryResponse{
			OrgId:         "org-123",
			RunId:         "run-1",
			WorkflowId:    "workflow-1",
			JobId:         req.GetJobId(),
			AttemptId:     "attempt-1",
			Attempt:       1,
			JobStatus:     "finished",
			AttemptStatus: "finished",
			HasSummary:    false,
			EmptyReason:   "no_summary",
		}, nil
	}

	var stderr bytes.Buffer
	cmd := NewCmdSummary()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output", "json", "job-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(&stderr)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty for json output", stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	if got["job_id"] != "job-1" || got["attempt_id"] != "attempt-1" || got["empty_reason"] != "no_summary" {
		t.Fatalf("unexpected JSON document: %+v", got)
	}
	if got["has_summary"] != false {
		t.Fatalf("has_summary = %#v, want false", got["has_summary"])
	}
	if got["step_count"] != float64(0) {
		t.Fatalf("step_count = %#v, want 0", got["step_count"])
	}
}

func TestSummaryBothNotFoundNamesUnresolvedID(t *testing.T) {
	restoreSummaryAPIs(t)

	ciGetJobSummary = func(ctx context.Context, token, orgID string, req *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
		if req.GetJobId() != "" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("job not found"))
		}
		return nil, connect.NewError(connect.CodeNotFound, errors.New("attempt not found"))
	}

	_, _, err := executeSummaryCommand("missing-id")
	if err == nil || !strings.Contains(err.Error(), `could not resolve "missing-id" as an attempt or job ID`) {
		t.Fatalf("err = %v", err)
	}
}

func TestSummaryJobUnavailableDoesNotTryAttempt(t *testing.T) {
	restoreSummaryAPIs(t)

	attemptCalled := false
	ciGetJobSummary = func(ctx context.Context, token, orgID string, req *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
		if req.GetAttemptId() != "" {
			attemptCalled = true
			return nil, nil
		}
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("storage unavailable"))
	}

	_, _, err := executeSummaryCommand("job-1")
	if err == nil || !strings.Contains(err.Error(), "failed to get job summary") {
		t.Fatalf("err = %v", err)
	}
	if attemptCalled {
		t.Fatal("attempt lookup should not run on unavailable job lookup")
	}
}

func TestSummaryRejectsUnsupportedOutputBeforeAuth(t *testing.T) {
	restoreSummaryAPIs(t)

	ciGetJobSummary = func(ctx context.Context, token, orgID string, req *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
		t.Fatal("summary API should not be called for invalid output")
		return nil, nil
	}

	cmd := NewCmdSummary()
	cmd.SetArgs([]string{"--output", "yaml", "attempt-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `unsupported output "yaml"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestSummaryJSONOutputRequiresIDWithoutPrintingHelp(t *testing.T) {
	restoreSummaryAPIs(t)

	ciGetJobSummary = func(ctx context.Context, token, orgID string, req *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
		t.Fatal("summary API should not be called without an ID")
		return nil, nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewCmdSummary()
	cmd.SetArgs([]string{"--output", "json"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "expected exactly one attempt or job ID") {
		t.Fatalf("err = %v", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want no help text", stderr.String())
	}
}

func executeSummaryCommand(id string) (string, string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewCmdSummary()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", id})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func restoreSummaryAPIs(t *testing.T) {
	t.Helper()

	originalGetJobSummary := ciGetJobSummary
	t.Cleanup(func() {
		ciGetJobSummary = originalGetJobSummary
	})
}

func summaryResponse(attemptID string, jobID string, markdown string) *civ1.GetJobSummaryResponse {
	return &civ1.GetJobSummaryResponse{
		OrgId:         "org-123",
		RunId:         "run-1",
		WorkflowId:    "workflow-1",
		JobId:         jobID,
		AttemptId:     attemptID,
		Attempt:       3,
		JobStatus:     "finished",
		AttemptStatus: "finished",
		HasSummary:    true,
		StepCount:     1,
		Markdown:      markdown,
	}
}
