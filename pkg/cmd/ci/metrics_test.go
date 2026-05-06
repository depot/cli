package ci

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func TestMetricsAttemptJSONOutput(t *testing.T) {
	originalGetAttemptMetrics := ciGetJobAttemptMetrics
	t.Cleanup(func() { ciGetJobAttemptMetrics = originalGetAttemptMetrics })

	var capturedAttemptID string
	ciGetJobAttemptMetrics = func(ctx context.Context, token, orgID, attemptID string) (*civ1.GetJobAttemptMetricsResponse, error) {
		capturedAttemptID = attemptID
		return attemptMetricsResponse(), nil
	}

	cmd := NewCmdMetrics()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output", "json", "att-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if capturedAttemptID != "att-1" {
		t.Fatalf("attemptID = %q, want att-1", capturedAttemptID)
	}

	var document map[string]any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	if document["type"] != "attempt" {
		t.Fatalf("type = %v, want attempt", document["type"])
	}
	attempt := document["attempt"].(map[string]any)
	samples := attempt["samples"].([]any)
	firstSample := samples[0].(map[string]any)
	if firstSample["cpu_utilization"] != 0.5 {
		t.Fatalf("cpu_utilization = %v, want 0.5", firstSample["cpu_utilization"])
	}
	if _, ok := firstSample["memory_utilization"]; ok {
		t.Fatalf("memory_utilization should be omitted for CPU-only sample: %s", stdout)
	}
}

func TestMetricsJobJSONOmitsChildSamples(t *testing.T) {
	originalGetJobMetrics := ciGetJobMetrics
	t.Cleanup(func() { ciGetJobMetrics = originalGetJobMetrics })

	ciGetJobMetrics = func(ctx context.Context, token, orgID, jobID string) (*civ1.GetJobMetricsResponse, error) {
		return jobMetricsResponse(), nil
	}

	cmd := NewCmdMetrics()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--job", "job-1", "--output", "json"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, `"samples"`) {
		t.Fatalf("job metrics JSON should not embed child samples:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"type": "job"`) {
		t.Fatalf("job metrics JSON missing type:\n%s", stdout)
	}
}

func TestMetricsRunJSONOmitsChildSamples(t *testing.T) {
	originalGetRunMetrics := ciGetRunMetrics
	t.Cleanup(func() { ciGetRunMetrics = originalGetRunMetrics })

	ciGetRunMetrics = func(ctx context.Context, token, orgID, runID string) (*civ1.GetRunMetricsResponse, error) {
		return runMetricsResponse(), nil
	}

	cmd := NewCmdMetrics()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--run", "run-1", "--output", "json"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, `"samples"`) {
		t.Fatalf("run metrics JSON should not embed child samples:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"type": "run"`) {
		t.Fatalf("run metrics JSON missing type:\n%s", stdout)
	}
}

func TestMetricsHumanJobOutputShowsDrillDownCommand(t *testing.T) {
	originalGetJobMetrics := ciGetJobMetrics
	t.Cleanup(func() { ciGetJobMetrics = originalGetJobMetrics })

	ciGetJobMetrics = func(ctx context.Context, token, orgID, jobID string) (*civ1.GetJobMetricsResponse, error) {
		return jobMetricsResponse(), nil
	}

	cmd := NewCmdMetrics()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--job", "job-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Metrics: depot ci metrics att-1 --org org-123") {
		t.Fatalf("human output missing drill-down command:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Full samples: depot ci metrics att-1 --org org-123 --output json") {
		t.Fatalf("human output missing full samples command:\n%s", stdout)
	}
}

func TestMetricsHumanAttemptOutputShowsFullSamplesCommand(t *testing.T) {
	originalGetAttemptMetrics := ciGetJobAttemptMetrics
	t.Cleanup(func() { ciGetJobAttemptMetrics = originalGetAttemptMetrics })

	ciGetJobAttemptMetrics = func(ctx context.Context, token, orgID, attemptID string) (*civ1.GetJobAttemptMetricsResponse, error) {
		return attemptMetricsResponse(), nil
	}

	cmd := NewCmdMetrics()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "att-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Samples: 2 returned / 2 raw") {
		t.Fatalf("human output missing sample count:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Full samples: depot ci metrics att-1 --org org-123 --output json") {
		t.Fatalf("human output missing full samples command:\n%s", stdout)
	}
}

func TestMetricsRunResourceExhaustedShowsActionableMessage(t *testing.T) {
	originalGetRunMetrics := ciGetRunMetrics
	t.Cleanup(func() { ciGetRunMetrics = originalGetRunMetrics })

	message := `This run has 198 attempts, which is too many to summarize safely.

Request metrics for a narrower scope, such as a single job or attempt.

Use GetRunStatus for run run-1 to find job and attempt IDs.`
	ciGetRunMetrics = func(ctx context.Context, token, orgID, runID string) (*civ1.GetRunMetricsResponse, error) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New(message))
	}

	cmd := NewCmdMetrics()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--run", "run-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected resource exhausted error")
	}
	want := `This run has 198 attempts, which is too many to summarize safely.

Try a narrower metrics request:
  depot ci metrics --job <job-id>
  depot ci metrics <attempt-id>

Use ` + "`depot ci status run-1`" + ` to find job and attempt IDs.`
	if err.Error() != want {
		t.Fatalf("err = %q, want actionable message", err.Error())
	}
}

func TestMetricsJobResourceExhaustedShowsActionableMessage(t *testing.T) {
	originalGetJobMetrics := ciGetJobMetrics
	t.Cleanup(func() { ciGetJobMetrics = originalGetJobMetrics })

	message := `This job has 51 attempts, which is too many to summarize safely.

Request metrics for a single attempt instead.

Use GetRunStatus for run run-1 to find attempt IDs.`
	ciGetJobMetrics = func(ctx context.Context, token, orgID, jobID string) (*civ1.GetJobMetricsResponse, error) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New(message))
	}

	cmd := NewCmdMetrics()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--job", "job-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected resource exhausted error")
	}
	want := `This job has 51 attempts, which is too many to summarize safely.

Try a narrower metrics request:
  depot ci metrics <attempt-id>

Use ` + "`depot ci status run-1`" + ` to find attempt IDs.`
	if err.Error() != want {
		t.Fatalf("err = %q, want actionable message", err.Error())
	}
}

func TestMetricsJSONHandlesMissingStatsAndCap(t *testing.T) {
	originalGetAttemptMetrics := ciGetJobAttemptMetrics
	t.Cleanup(func() { ciGetJobAttemptMetrics = originalGetAttemptMetrics })

	resp := attemptMetricsResponse()
	resp.Attempt.Availability = &civ1.CIMetricsAvailability{
		Code:   civ1.CIMetricsAvailabilityCode_CI_METRICS_AVAILABILITY_CODE_NO_SAMPLES,
		Reason: "no_samples",
	}
	resp.Attempt.Stats = nil
	resp.Attempt.Cap = nil
	resp.Attempt.Samples = nil
	ciGetJobAttemptMetrics = func(ctx context.Context, token, orgID, attemptID string) (*civ1.GetJobAttemptMetricsResponse, error) {
		return resp, nil
	}

	cmd := NewCmdMetrics()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output", "json", "att-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"reason": "no_samples"`) {
		t.Fatalf("JSON output missing availability reason:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"sample_count": 0`) {
		t.Fatalf("JSON output missing zero stats fallback:\n%s", stdout)
	}
}

func TestMetricsTextHandlesMissingStats(t *testing.T) {
	originalGetAttemptMetrics := ciGetJobAttemptMetrics
	t.Cleanup(func() { ciGetJobAttemptMetrics = originalGetAttemptMetrics })

	resp := attemptMetricsResponse()
	resp.Attempt.Availability = &civ1.CIMetricsAvailability{
		Code:   civ1.CIMetricsAvailabilityCode_CI_METRICS_AVAILABILITY_CODE_NO_SAMPLES,
		Reason: "no_samples",
	}
	resp.Attempt.Stats = nil
	ciGetJobAttemptMetrics = func(ctx context.Context, token, orgID, attemptID string) (*civ1.GetJobAttemptMetricsResponse, error) {
		return resp, nil
	}

	cmd := NewCmdMetrics()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "att-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Availability: no_samples") {
		t.Fatalf("text output missing availability:\n%s", stdout)
	}
}

func TestMetricsRejectsAmbiguousSelectionBeforeAPIRequest(t *testing.T) {
	originalGetAttemptMetrics := ciGetJobAttemptMetrics
	t.Cleanup(func() { ciGetJobAttemptMetrics = originalGetAttemptMetrics })

	called := false
	ciGetJobAttemptMetrics = func(ctx context.Context, token, orgID, attemptID string) (*civ1.GetJobAttemptMetricsResponse, error) {
		called = true
		return nil, nil
	}

	cmd := NewCmdMetrics()
	cmd.SetArgs([]string{"--token", "token-123", "--attempt", "att-1", "--job", "job-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want mutually exclusive selection error", err)
	}
	if called {
		t.Fatal("API should not be called for invalid selection")
	}
}

func TestMetricsBareNotFoundSuggestsJobAndRunFlags(t *testing.T) {
	originalGetAttemptMetrics := ciGetJobAttemptMetrics
	t.Cleanup(func() { ciGetJobAttemptMetrics = originalGetAttemptMetrics })

	ciGetJobAttemptMetrics = func(ctx context.Context, token, orgID, attemptID string) (*civ1.GetJobAttemptMetricsResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("not found"))
	}

	cmd := NewCmdMetrics()
	cmd.SetArgs([]string{"--token", "token-123", "job-looks-like-id"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "use --job <job-id> or --run <run-id>") {
		t.Fatalf("err = %v, want actionable job/run hint", err)
	}
}

func attemptMetricsResponse() *civ1.GetJobAttemptMetricsResponse {
	return &civ1.GetJobAttemptMetricsResponse{
		SnapshotAt: "2026-05-03T12:01:00Z",
		Run:        metricsRun(),
		Workflow:   metricsWorkflow(),
		Job:        metricsJob(),
		Attempt: &civ1.CIMetricsAttemptMetrics{
			Attempt:      metricsAttempt(),
			Availability: availableMetrics(),
			Stats:        metricsStats(),
			Cap:          metricsCap(),
			Samples: []*civ1.CIMetricSample{
				{Timestamp: "2026-05-03T12:00:00Z", CpuUtilization: float64Ptr(0.5)},
				{Timestamp: "2026-05-03T12:00:01Z", MemoryUtilization: float64Ptr(0.75)},
			},
		},
	}
}

func jobMetricsResponse() *civ1.GetJobMetricsResponse {
	return &civ1.GetJobMetricsResponse{
		SnapshotAt: "2026-05-03T12:01:00Z",
		Run:        metricsRun(),
		Workflow:   metricsWorkflow(),
		Job:        metricsJob(),
		Attempts: []*civ1.CIMetricsAttemptSummary{
			{
				Attempt:      metricsAttempt(),
				Availability: availableMetrics(),
				Stats:        metricsStats(),
				Cap:          metricsCap(),
			},
		},
	}
}

func runMetricsResponse() *civ1.GetRunMetricsResponse {
	return &civ1.GetRunMetricsResponse{
		SnapshotAt: "2026-05-03T12:01:00Z",
		Run:        metricsRun(),
		Workflows: []*civ1.CIMetricsWorkflowMetrics{
			{
				Workflow: metricsWorkflow(),
				Jobs: []*civ1.CIMetricsJobMetrics{
					{
						Job: metricsJob(),
						Attempts: []*civ1.CIMetricsAttemptSummary{
							{
								Attempt:      metricsAttempt(),
								Availability: availableMetrics(),
								Stats:        metricsStats(),
								Cap:          metricsCap(),
							},
						},
					},
				},
			},
		},
	}
}

func metricsRun() *civ1.CIMetricsRunContext {
	return &civ1.CIMetricsRunContext{RunId: "run-1", Repo: "depot/api", Status: "running"}
}

func metricsWorkflow() *civ1.CIMetricsWorkflowContext {
	return &civ1.CIMetricsWorkflowContext{WorkflowId: "workflow-1", Name: "CI", Status: "running"}
}

func metricsJob() *civ1.CIMetricsJobContext {
	return &civ1.CIMetricsJobContext{JobId: "job-1", JobKey: "build", Status: "running", CurrentAttempt: 1}
}

func metricsAttempt() *civ1.CIMetricsAttemptContext {
	return &civ1.CIMetricsAttemptContext{AttemptId: "att-1", Attempt: 1, Status: "running", SandboxId: "sandbox-1"}
}

func availableMetrics() *civ1.CIMetricsAvailability {
	return &civ1.CIMetricsAvailability{
		Code:   civ1.CIMetricsAvailabilityCode_CI_METRICS_AVAILABILITY_CODE_AVAILABLE,
		Reason: "available",
	}
}

func metricsStats() *civ1.CIMetricsStats {
	return &civ1.CIMetricsStats{
		SampleCount:              2,
		CpuSampleCount:           1,
		MemorySampleCount:        1,
		PeakCpuUtilization:       float64Ptr(0.5),
		AverageCpuUtilization:    float64Ptr(0.5),
		PeakMemoryUtilization:    float64Ptr(0.75),
		AverageMemoryUtilization: float64Ptr(0.75),
		ObservedStartedAt:        "2026-05-03T12:00:00Z",
		ObservedFinishedAt:       "2026-05-03T12:00:01Z",
	}
}

func metricsCap() *civ1.CIMetricsCapMetadata {
	return &civ1.CIMetricsCapMetadata{
		RawSampleCount:         2,
		ReturnedSampleCount:    2,
		MaxReturnedSampleCount: 5000,
	}
}

func float64Ptr(value float64) *float64 {
	return &value
}
