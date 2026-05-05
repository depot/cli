package ci

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestResolveLogTargetJSONOptionsReturnNonInteractiveAmbiguity(t *testing.T) {
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

	options := logTargetResolutionOptionsForOutput(logOutputOptions{output: logOutputJSON})
	if options.allowInteractive {
		t.Fatal("json output should disable interactive job resolution")
	}

	_, err := resolveLogTargetWithOptions(resp, "run-1", "", "", options)
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(err.Error(), "run has multiple jobs, specify one with --job") {
		t.Fatalf("expected multiple-jobs ambiguity error, got: %v", err)
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

func TestLogsCommandHistoricalDirectAttemptDefaultOutputUnchanged(t *testing.T) {
	originalGetRunStatus := ciGetRunStatus
	originalGetJobAttemptLogs := ciGetJobAttemptLogs
	t.Cleanup(func() {
		ciGetRunStatus = originalGetRunStatus
		ciGetJobAttemptLogs = originalGetJobAttemptLogs
	})

	ciGetRunStatus = func(context.Context, string, string, string) (*civ1.GetRunStatusResponse, error) {
		return nil, errors.New("not a run")
	}
	ciGetJobAttemptLogs = func(_ context.Context, _, _, attemptID string) ([]*civ1.LogLine, error) {
		if attemptID != "attempt-1" {
			t.Fatalf("attemptID = %q, want attempt-1", attemptID)
		}
		return []*civ1.LogLine{
			{Body: "first"},
			{Body: "second"},
		}, nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewCmdLogs()
	cmd.SetArgs([]string{"attempt-1", "--token", "token-123", "--org", "org-123"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "first\nsecond\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestLogsCommandHistoricalJSONSuppressesHumanTargetMessages(t *testing.T) {
	originalGetRunStatus := ciGetRunStatus
	originalGetJobAttemptLogs := ciGetJobAttemptLogs
	t.Cleanup(func() {
		ciGetRunStatus = originalGetRunStatus
		ciGetJobAttemptLogs = originalGetJobAttemptLogs
	})

	ciGetRunStatus = func(context.Context, string, string, string) (*civ1.GetRunStatusResponse, error) {
		return &civ1.GetRunStatusResponse{
			RunId: "run-1",
			Workflows: []*civ1.WorkflowStatus{
				{
					WorkflowPath: ".depot/workflows/ci.yml",
					Jobs: []*civ1.JobStatus{
						{
							JobId:  "job-1",
							JobKey: "ci.yml:build",
							Status: "finished",
							Attempts: []*civ1.AttemptStatus{
								{AttemptId: "attempt-1", Attempt: 1, Status: "finished"},
							},
						},
					},
				},
			},
		}, nil
	}
	ciGetJobAttemptLogs = func(context.Context, string, string, string) ([]*civ1.LogLine, error) {
		return []*civ1.LogLine{
			testCmdLogLine("step-1", 7, 123, 1, `quoted "body" \ path`),
		}, nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewCmdLogs()
	cmd.SetArgs([]string{"run-1", "--token", "token-123", "--org", "org-123", "-o", "json"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	events := decodeLogEvents(t, stdout.String())
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: %s", len(events), stdout.String())
	}
	assertLogLineEvent(t, events[0], map[string]any{
		"type":         "line",
		"timestamp":    "1970-01-01T00:00:00.123Z",
		"timestamp_ms": float64(123),
		"stream":       "stderr",
		"step_key":     "step-1",
		"line_number":  float64(7),
		"body":         `quoted "body" \ path`,
	})
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestPrintLogLinesDefaultOutputUnchanged(t *testing.T) {
	lines := []*civ1.LogLine{
		{Body: "first"},
		{Body: "second"},
	}

	var out bytes.Buffer
	if err := printLogLines(&out, lines, logOutputOptions{}); err != nil {
		t.Fatal(err)
	}

	if got, want := out.String(), "first\nsecond\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPrintLogLinesTimestamps(t *testing.T) {
	lines := []*civ1.LogLine{
		{TimestampMs: 0, Body: "first"},
		{TimestampMs: 123, Body: "second"},
	}

	var out bytes.Buffer
	if err := printLogLines(&out, lines, logOutputOptions{timestamps: true}); err != nil {
		t.Fatal(err)
	}

	want := "1970-01-01T00:00:00Z first\n1970-01-01T00:00:00.123Z second\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPrintLogLinesJSON(t *testing.T) {
	largeBody := strings.Repeat("x", 5000) + ` "quoted" \ slash`
	lines := []*civ1.LogLine{
		testCmdLogLine("step-1", 7, 123, 1, largeBody),
	}

	var out bytes.Buffer
	if err := printLogLines(&out, lines, logOutputOptions{output: logOutputJSON}); err != nil {
		t.Fatal(err)
	}

	events := decodeLogEvents(t, out.String())
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: %s", len(events), out.String())
	}
	assertLogLineEvent(t, events[0], map[string]any{
		"type":         "line",
		"timestamp":    "1970-01-01T00:00:00.123Z",
		"timestamp_ms": float64(123),
		"stream":       "stderr",
		"step_key":     "step-1",
		"line_number":  float64(7),
		"body":         largeBody,
	})
}

func TestPrintLogLinesJSONIgnoresTimestampsOption(t *testing.T) {
	lines := []*civ1.LogLine{
		testCmdLogLine("step-1", 7, 123, 1, "body"),
	}

	var jsonOnly bytes.Buffer
	if err := printLogLines(&jsonOnly, lines, logOutputOptions{output: logOutputJSON}); err != nil {
		t.Fatal(err)
	}

	var jsonWithTimestamps bytes.Buffer
	if err := printLogLines(&jsonWithTimestamps, lines, logOutputOptions{output: logOutputJSON, timestamps: true}); err != nil {
		t.Fatal(err)
	}

	if jsonWithTimestamps.String() != jsonOnly.String() {
		t.Fatalf("json with timestamps differs:\nwithout: %s\nwith: %s", jsonOnly.String(), jsonWithTimestamps.String())
	}
}

func TestLogOutputOptionsValidateRejectsUnsupportedOutput(t *testing.T) {
	err := (logOutputOptions{output: "yaml"}).validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), `unsupported output "yaml"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLogsCommandRejectsUnsupportedOutputBeforeAuth(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewCmdLogs()
	cmd.SetArgs([]string{"attempt-1", "--output", "yaml"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `unsupported output "yaml"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStreamLogTargetWithFollowUXTimestamps(t *testing.T) {
	original := ciStreamJobAttemptLogLines
	t.Cleanup(func() { ciStreamJobAttemptLogLines = original })

	ciStreamJobAttemptLogLines = func(
		_ context.Context,
		_, _ string,
		target api.CILogStreamTarget,
		onLine func(*civ1.LogLine) error,
		onStatus func(string) error,
	) error {
		if target.AttemptID != "attempt-1" {
			t.Fatalf("attemptID = %q, want attempt-1", target.AttemptID)
		}
		if err := onStatus("running"); err != nil {
			return err
		}
		return onLine(testCmdLogLine("step-1", 1, 123, 0, "build"))
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := streamLogTargetWithFollowUX(
		context.Background(),
		"token-123",
		"org-123",
		api.CILogStreamTarget{AttemptID: "attempt-1"},
		logTarget{attemptID: "attempt-1", attemptStatus: "queued"},
		&stdout,
		newLogFollowReporter(&stderr, false),
		logOutputOptions{timestamps: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := stdout.String(), "1970-01-01T00:00:00.123Z build\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if strings.Contains(stderr.String(), "1970-01-01") {
		t.Fatalf("status output should not be timestamp-prefixed: %q", stderr.String())
	}
}

func TestStreamLogTargetWithFollowUXJSONEmitsStatusLineAndEnd(t *testing.T) {
	original := ciStreamJobAttemptLogLines
	t.Cleanup(func() { ciStreamJobAttemptLogLines = original })

	ciStreamJobAttemptLogLines = func(
		_ context.Context,
		_, _ string,
		target api.CILogStreamTarget,
		onLine func(*civ1.LogLine) error,
		onStatus func(string) error,
	) error {
		if target.AttemptID != "attempt-1" {
			t.Fatalf("attemptID = %q, want attempt-1", target.AttemptID)
		}
		if err := onStatus("running"); err != nil {
			return err
		}
		if err := onStatus("running"); err != nil {
			return err
		}
		if err := onLine(testCmdLogLine("step-1", 1, 123, 0, "build")); err != nil {
			return err
		}
		return onStatus("finished")
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := streamLogTargetWithFollowUX(
		context.Background(),
		"token-123",
		"org-123",
		api.CILogStreamTarget{AttemptID: "attempt-1"},
		logTarget{attemptID: "attempt-1", attemptStatus: "running"},
		&stdout,
		newLogFollowReporter(&stderr, false),
		logOutputOptions{output: logOutputJSON},
	)
	if err != nil {
		t.Fatal(err)
	}

	events := decodeLogEvents(t, stdout.String())
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4: %s", len(events), stdout.String())
	}
	assertEventFields(t, events[0], map[string]any{"type": "status", "status": "running"})
	assertLogLineEvent(t, events[1], map[string]any{
		"type":         "line",
		"timestamp":    "1970-01-01T00:00:00.123Z",
		"timestamp_ms": float64(123),
		"stream":       "stdout",
		"step_key":     "step-1",
		"line_number":  float64(1),
		"body":         "build",
	})
	assertEventFields(t, events[2], map[string]any{"type": "status", "status": "finished"})
	assertEventFields(t, events[3], map[string]any{"type": "end", "status": "finished", "line_count": float64(1)})
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func testCmdLogLine(stepID string, lineNumber uint32, timestampMs int64, stream uint32, body string) *civ1.LogLine {
	return &civ1.LogLine{
		StepId:      stepID,
		TimestampMs: timestampMs,
		LineNumber:  lineNumber,
		Stream:      stream,
		Body:        body,
	}
}

func decodeLogEvents(t *testing.T, output string) []map[string]any {
	t.Helper()

	dec := json.NewDecoder(strings.NewReader(output))
	var events []map[string]any
	for {
		var event map[string]any
		err := dec.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("invalid NDJSON: %v\n%s", err, output)
		}
		events = append(events, event)
	}
	return events
}

func assertLogLineEvent(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()

	assertEventFields(t, got, want)
	if _, ok := got["step_id"]; ok {
		t.Fatalf("line event should not include step_id: %#v", got)
	}
	if _, ok := got["step_name"]; ok {
		t.Fatalf("line event should not include step_name: %#v", got)
	}
}

func assertEventFields(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()

	for key, wantValue := range want {
		if gotValue := got[key]; gotValue != wantValue {
			t.Fatalf("%s = %#v, want %#v in event %#v", key, gotValue, wantValue, got)
		}
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

func TestResolveLogTargetWithFollowRetryStopsReporterOnCancellation(t *testing.T) {
	reporter := newLogFollowReporter(io.Discard, true)
	t.Cleanup(reporter.Stop)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := resolveLogTargetWithFollowRetry(
		ctx,
		"token-123",
		"org-123",
		"run-123",
		"",
		"",
		&pendingLogTargetError{message: "Waiting for job to start..."},
		reporter,
		logTargetResolutionOptions{allowInteractive: true},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}

	reporter.mu.Lock()
	active := reporter.spinner != nil
	lastStatus := reporter.lastStatus
	reporter.mu.Unlock()

	if active {
		t.Fatal("spinner should stop when retry resolution is cancelled")
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
		logOutputOptions{},
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
		logOutputOptions{},
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
		logOutputOptions{},
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
