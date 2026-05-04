package ci

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/depot/cli/pkg/api"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func TestFindLogsJob_AutoSelectSingleJob(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "build", Status: "finished"},
				},
			},
		},
	}

	job, path, err := findLogsJob(resp, "run-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobKey != "build" {
		t.Fatalf("expected job key %q, got %q", "build", job.JobKey)
	}
	if path != ".depot/workflows/ci.yml" {
		t.Fatalf("expected workflow path %q, got %q", ".depot/workflows/ci.yml", path)
	}
}

func TestFindLogsJob_MultipleJobsRequiresFlag(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "build", Status: "finished"},
					{JobId: "job-2", JobKey: "test", Status: "running"},
				},
			},
		},
	}

	_, _, err := findLogsJob(resp, "run-1", "", "")
	if err == nil {
		t.Fatal("expected error for multiple jobs without --job flag")
	}
}

func TestFindLogsJob_MatchByJobKey(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "build", Status: "finished"},
					{JobId: "job-2", JobKey: "test", Status: "running"},
				},
			},
		},
	}

	job, _, err := findLogsJob(resp, "run-1", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-2" {
		t.Fatalf("expected job ID %q, got %q", "job-2", job.JobId)
	}
}

func TestFindLogsJob_MatchByJobID(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "build", Status: "finished"},
					{JobId: "job-2", JobKey: "test", Status: "running"},
				},
			},
		},
	}

	job, _, err := findLogsJob(resp, "job-2", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobKey != "test" {
		t.Fatalf("expected job key %q, got %q", "test", job.JobKey)
	}
}

func TestResolveLogTarget_DirectJobIDUsesJobIDStreamTarget(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{
						JobId:  "job-2",
						JobKey: "test",
						Status: "running",
						Attempts: []*civ1.AttemptStatus{
							{AttemptId: "att-2", Attempt: 2, Status: "running"},
						},
					},
				},
			},
		},
	}

	target, err := resolveLogTarget(resp, "job-2", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if target.streamJobID != "job-2" {
		t.Fatalf("expected stream job ID %q, got %q", "job-2", target.streamJobID)
	}
	if target.attemptID != "att-2" {
		t.Fatalf("expected display attempt ID %q, got %q", "att-2", target.attemptID)
	}
}

func TestFindLogsJob_DuplicateJobKeyRequiresWorkflow(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "build", Status: "finished"},
				},
			},
			{
				WorkflowPath: ".depot/workflows/release.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-2", JobKey: "build", Status: "queued"},
				},
			},
		},
	}

	_, _, err := findLogsJob(resp, "run-1", "build", "")
	if err == nil {
		t.Fatal("expected error for duplicate job key without --workflow")
	}
}

func TestFindLogsJob_DuplicateJobKeyWithWorkflowFilter(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "build", Status: "finished"},
				},
			},
			{
				WorkflowPath: ".depot/workflows/release.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-2", JobKey: "build", Status: "queued"},
				},
			},
		},
	}

	job, path, err := findLogsJob(resp, "run-1", "build", "ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-1" {
		t.Fatalf("expected job ID %q, got %q", "job-1", job.JobId)
	}
	if path != ".depot/workflows/ci.yml" {
		t.Fatalf("expected workflow path %q, got %q", ".depot/workflows/ci.yml", path)
	}
}

func TestFindLogsJob_WorkflowFilterNoMatch(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "build", Status: "finished"},
				},
			},
		},
	}

	_, _, err := findLogsJob(resp, "run-1", "", "release.yml")
	if err == nil {
		t.Fatal("expected error for non-matching workflow filter")
	}
}

func TestResolveLogTarget_NoAttempts(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "build", Status: "queued"},
				},
			},
		},
	}

	_, err := resolveLogTarget(resp, "run-1", "", "")
	if err == nil {
		t.Fatal("expected error for job with no attempts")
	}
	if !isFollowRetryableResolutionError(err) {
		t.Fatalf("expected no-attempt error to be retryable for --follow, got: %v", err)
	}
}

func TestResolveLogTarget_QueuedRunWithoutJobsIsPending(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId:  "run-1",
		Status: "queued",
	}

	_, err := resolveLogTarget(resp, "run-1", "", "")
	if err == nil {
		t.Fatal("expected pending error")
	}
	pending, ok := err.(*pendingLogTargetError)
	if !ok {
		t.Fatalf("expected pendingLogTargetError, got %T: %v", err, err)
	}
	want := "Waiting for jobs to be created (run status: queued)..."
	if pending.Error() != want {
		t.Fatalf("expected %q, got %q", want, pending.Error())
	}
}

func TestResolveLogTarget_TerminalRunWithoutJobsReturnsNoLogsMessage(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId:  "run-1",
		Status: "finished",
	}

	target, err := resolveLogTarget(resp, "run-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "run run-1 has no jobs (run status: finished); no logs were produced."
	if target.noLogsMessage != want {
		t.Fatalf("expected %q, got %q", want, target.noLogsMessage)
	}
}

func TestResolveLogTarget_QueuedJobWithoutAttemptsIsPending(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId:  "run-1",
		Status: "running",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "ci.yml:build", Status: "queued"},
				},
			},
		},
	}

	_, err := resolveLogTarget(resp, "run-1", "", "")
	if err == nil {
		t.Fatal("expected pending error")
	}
	pending, ok := err.(*pendingLogTargetError)
	if !ok {
		t.Fatalf("expected pendingLogTargetError, got %T: %v", err, err)
	}
	want := `Waiting for job "build" to start (status: queued)...`
	if pending.Error() != want {
		t.Fatalf("expected %q, got %q", want, pending.Error())
	}
}

func TestResolveLogTarget_SkippedJobWithoutAttemptsReturnsNoLogsMessage(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId:  "run-1",
		Status: "finished",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "ci.yml:build", Status: "skipped"},
				},
			},
		},
	}

	target, err := resolveLogTarget(resp, "run-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := `Job "build" was skipped; no logs were produced.`
	if target.noLogsMessage != want {
		t.Fatalf("expected %q, got %q", want, target.noLogsMessage)
	}
}

func TestFollowRetryableResolutionError(t *testing.T) {
	if !isFollowRetryableResolutionError(&pendingLogTargetError{message: "Waiting for job to start..."}) {
		t.Fatal("pending log target errors should be retryable")
	}

	if isFollowRetryableResolutionError(errors.New(`job "deploy" not found`)) {
		t.Fatal("plain errors should not be retryable")
	}
}

func TestNoStreamLogsReceivedMessage(t *testing.T) {
	target := logTarget{attemptID: "att-1", attemptStatus: "failed", jobKey: "ci.yml:build"}
	want := `Log stream ended for job "build" (status: failed); no logs were produced.`
	if got := noStreamLogsReceivedMessage(target); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestLogStreamWaitingMessageIncludesStatus(t *testing.T) {
	target := logTarget{attemptID: "att-1", attemptStatus: "running"}
	want := "Waiting for logs from attempt att-1 (status: running)..."
	if got := logStreamWaitingMessage(target); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestLogStreamWaitingMessageIncludesUnresolvedStatus(t *testing.T) {
	target := logTarget{attemptStatus: "running"}
	want := "Waiting for logs (status: running)..."
	if got := logStreamWaitingMessage(target); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestLogFollowReporterRestartsWaitingAfterIdleLogs(t *testing.T) {
	reporter := newLogFollowReporter(io.Discard, true)
	reporter.idleDelay = 10 * time.Millisecond
	t.Cleanup(reporter.Stop)

	reporter.Status("Waiting for logs (status: running)...")
	reporter.SawLogs()

	reporter.mu.Lock()
	activeImmediately := reporter.spinner != nil
	reporter.mu.Unlock()
	if activeImmediately {
		t.Fatal("spinner should stop while logs are being written")
	}

	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		reporter.mu.Lock()
		active := reporter.spinner != nil
		reporter.mu.Unlock()
		if active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("spinner did not restart after logs went idle")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLogFollowReporterStopPreventsIdleRestart(t *testing.T) {
	reporter := newLogFollowReporter(io.Discard, true)
	reporter.idleDelay = 10 * time.Millisecond

	reporter.Status("Waiting for logs (status: running)...")
	reporter.SawLogs()
	reporter.Stop()

	time.Sleep(50 * time.Millisecond)

	reporter.mu.Lock()
	active := reporter.spinner != nil
	lastStatus := reporter.lastStatus
	reporter.mu.Unlock()

	if active {
		t.Fatal("spinner restarted after Stop")
	}
	if lastStatus != "" {
		t.Fatalf("lastStatus = %q, want empty", lastStatus)
	}
}

func TestStreamUnresolvedLogsWithFollowUXTriesJobThenAttempt(t *testing.T) {
	original := ciStreamJobAttemptLogs
	t.Cleanup(func() { ciStreamJobAttemptLogs = original })

	var calls []api.CILogStreamTarget
	ciStreamJobAttemptLogs = func(
		_ context.Context,
		_, _ string,
		target api.CILogStreamTarget,
		w io.Writer,
		_ func(string),
	) error {
		calls = append(calls, target)
		if target.JobID != "" {
			return errors.New("job not found")
		}
		_, err := w.Write([]byte("hello\n"))
		return err
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := streamUnresolvedLogsWithFollowUX(
		context.Background(),
		"token-123",
		"org-123",
		"id-123",
		&stdout,
		newLogFollowReporter(&stderr, false),
	)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := stdout.String(), "hello\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].JobID != "id-123" || calls[0].AttemptID != "" {
		t.Fatalf("first call = %+v, want job target", calls[0])
	}
	if calls[1].AttemptID != "id-123" || calls[1].JobID != "" {
		t.Fatalf("second call = %+v, want attempt target", calls[1])
	}
	if strings.Contains(stderr.String(), "Following logs for attempt") {
		t.Fatalf("stderr should not classify unresolved ID as attempt: %q", stderr.String())
	}
}

func TestStreamUnresolvedLogsWithFollowUXPropagatesCancellation(t *testing.T) {
	original := ciStreamJobAttemptLogs
	t.Cleanup(func() { ciStreamJobAttemptLogs = original })

	var calls []api.CILogStreamTarget
	ciStreamJobAttemptLogs = func(
		_ context.Context,
		_, _ string,
		target api.CILogStreamTarget,
		_ io.Writer,
		_ func(string),
	) error {
		calls = append(calls, target)
		return context.Canceled
	}

	err := streamUnresolvedLogsWithFollowUX(
		context.Background(),
		"token-123",
		"org-123",
		"id-123",
		io.Discard,
		newLogFollowReporter(io.Discard, false),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].JobID != "id-123" || calls[0].AttemptID != "" {
		t.Fatalf("first call = %+v, want job target", calls[0])
	}
}

func TestStreamUnresolvedLogsWithFollowUXReturnsBothTargetErrors(t *testing.T) {
	original := ciStreamJobAttemptLogs
	t.Cleanup(func() { ciStreamJobAttemptLogs = original })

	ciStreamJobAttemptLogs = func(
		_ context.Context,
		_, _ string,
		target api.CILogStreamTarget,
		_ io.Writer,
		_ func(string),
	) error {
		if target.JobID != "" {
			return errors.New("job failed")
		}
		return errors.New("attempt failed")
	}

	err := streamUnresolvedLogsWithFollowUX(
		context.Background(),
		"token-123",
		"org-123",
		"id-123",
		io.Discard,
		newLogFollowReporter(io.Discard, false),
	)
	if err == nil {
		t.Fatal("expected error")
	}
	unresolvedErr, ok := err.(*unresolvedLogStreamError)
	if !ok {
		t.Fatalf("expected unresolvedLogStreamError, got %T", err)
	}
	if unresolvedErr.jobErr.Error() != "job failed" {
		t.Fatalf("job error = %v", unresolvedErr.jobErr)
	}
	if unresolvedErr.attemptErr.Error() != "attempt failed" {
		t.Fatalf("attempt error = %v", unresolvedErr.attemptErr)
	}
}

func TestFindLogsJob_SuffixMatch(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "ci.yml:build", Status: "finished"},
					{JobId: "job-2", JobKey: "ci.yml:test", Status: "running"},
				},
			},
		},
	}

	job, _, err := findLogsJob(resp, "run-1", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-2" {
		t.Fatalf("expected job ID %q, got %q", "job-2", job.JobId)
	}
}

func TestFindLogsJob_SuffixMatchAmbiguous(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "ci.yml:build", Status: "finished"},
				},
			},
			{
				WorkflowPath: ".depot/workflows/release.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-2", JobKey: "release.yml:build", Status: "queued"},
				},
			},
		},
	}

	_, _, err := findLogsJob(resp, "run-1", "build", "")
	if err == nil {
		t.Fatal("expected error for ambiguous suffix match across workflows")
	}
}

func TestJobDisplayNames_UniqueShortNames(t *testing.T) {
	candidates := []jobCandidate{
		{job: &civ1.JobStatus{JobKey: "ci.yml:build"}},
		{job: &civ1.JobStatus{JobKey: "ci.yml:test"}},
	}
	names := jobDisplayNames(candidates)
	if names["ci.yml:build"] != "build" {
		t.Fatalf("expected %q, got %q", "build", names["ci.yml:build"])
	}
	if names["ci.yml:test"] != "test" {
		t.Fatalf("expected %q, got %q", "test", names["ci.yml:test"])
	}
}

func TestJobDisplayNames_ConflictingShortNames(t *testing.T) {
	candidates := []jobCandidate{
		{job: &civ1.JobStatus{JobKey: "ci.yml:build"}},
		{job: &civ1.JobStatus{JobKey: "release.yml:build"}},
		{job: &civ1.JobStatus{JobKey: "ci.yml:test"}},
	}
	names := jobDisplayNames(candidates)
	// "build" conflicts, so both should use full key.
	if names["ci.yml:build"] != "ci.yml:build" {
		t.Fatalf("expected %q, got %q", "ci.yml:build", names["ci.yml:build"])
	}
	if names["release.yml:build"] != "release.yml:build" {
		t.Fatalf("expected %q, got %q", "release.yml:build", names["release.yml:build"])
	}
	// "test" is unique, so short name.
	if names["ci.yml:test"] != "test" {
		t.Fatalf("expected %q, got %q", "test", names["ci.yml:test"])
	}
}

func TestJobDisplayNames_NoColon(t *testing.T) {
	candidates := []jobCandidate{
		{job: &civ1.JobStatus{JobKey: "build"}},
		{job: &civ1.JobStatus{JobKey: "test"}},
	}
	names := jobDisplayNames(candidates)
	if names["build"] != "build" {
		t.Fatalf("expected %q, got %q", "build", names["build"])
	}
}

func TestFindLogsJob_SegmentMatch_ReusableWorkflow(t *testing.T) {
	// Reusable workflow: user passes "bazel" but key is "pr.yaml:bazel:build"
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/pr.yaml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "pr.yaml:detect_changes:build", Status: "finished"},
					{JobId: "job-2", JobKey: "pr.yaml:bazel:build", Status: "running"},
					{JobId: "job-3", JobKey: "pr.yaml:test_dashboard:test", Status: "queued"},
				},
			},
		},
	}

	job, _, err := findLogsJob(resp, "run-1", "bazel", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-2" {
		t.Fatalf("expected job ID %q, got %q", "job-2", job.JobId)
	}
}

func TestFindLogsJob_SegmentMatch_InlineWorkflow(t *testing.T) {
	// CLI-triggered run: key is "_inline_0.yaml:bazel:build"
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: "",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "_inline_0.yaml:bazel:build", Status: "running"},
				},
			},
		},
	}

	job, _, err := findLogsJob(resp, "run-1", "bazel", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-1" {
		t.Fatalf("expected job ID %q, got %q", "job-1", job.JobId)
	}
}

func TestFindLogsJob_SegmentMatch_AmbiguousSameWorkflow(t *testing.T) {
	// Parent job expands to multiple nested jobs within the same workflow.
	// Error should suggest a more specific --job, not --workflow.
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-1", JobKey: "ci.yml:backend:build", Status: "running"},
					{JobId: "job-2", JobKey: "ci.yml:backend:test", Status: "running"},
				},
			},
		},
	}

	_, _, err := findLogsJob(resp, "run-1", "backend", "")
	if err == nil {
		t.Fatal("expected error for ambiguous segment match")
	}
	if strings.Contains(err.Error(), "--workflow") {
		t.Fatalf("error should suggest --job, not --workflow: %v", err)
	}
	if !strings.Contains(err.Error(), "more specific --job") {
		t.Fatalf("expected 'more specific --job' hint, got: %v", err)
	}
}

func TestFindLogsJob_ShortPreferredOverSegment(t *testing.T) {
	// Short name match should take priority over segment match
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{JobId: "job-segment", JobKey: "ci.yml:build:test", Status: "running"},
					{JobId: "job-short", JobKey: "ci.yml:build", Status: "running"},
				},
			},
		},
	}

	// "build" short-matches "ci.yml:build" and segment-matches "ci.yml:build:test"
	job, _, err := findLogsJob(resp, "run-1", "build", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-short" {
		t.Fatalf("expected short match (job-short), got %q", job.JobId)
	}
}

func TestJobKeyShort_MultipleColons(t *testing.T) {
	got := jobKeyShort("ci.yml:foo:bar")
	if got != "foo:bar" {
		t.Fatalf("expected %q, got %q", "foo:bar", got)
	}
}

func TestWorkflowPathMatches(t *testing.T) {
	tests := []struct {
		path   string
		filter string
		want   bool
	}{
		{".depot/workflows/ci.yml", "ci.yml", true},
		{".depot/workflows/ci.yml", ".depot/workflows/ci.yml", true},
		{".depot/workflows/ci.yml", "release.yml", false},
		{"ci.yml", "ci.yml", true},
	}

	for _, tt := range tests {
		got := workflowPathMatches(tt.path, tt.filter)
		if got != tt.want {
			t.Errorf("workflowPathMatches(%q, %q) = %v, want %v", tt.path, tt.filter, got, tt.want)
		}
	}
}
