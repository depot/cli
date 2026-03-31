package ci

import (
	"testing"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func TestMatchJobKey(t *testing.T) {
	tests := []struct {
		jobKey  string
		userKey string
		want    int // 0=no match, 1=exact, 2=suffix, 3=segment
	}{
		// Exact match
		{"build", "build", 1},
		// Suffix match (last segment after colon)
		{"_inline_0.yaml:lint_typecheck", "lint_typecheck", 2},
		{"pr.yaml:bazel:build", "build", 2},
		{"pr.yaml:bazel:build", "bazel:build", 2},
		// Segment match (intermediate segment)
		{"pr.yaml:bazel:build", "bazel", 3},
		{"_inline_0.yaml:bazel:build", "bazel", 3},
		{"pr.yaml:bazel:build", "pr.yaml", 3},
		// No match
		{"pr.yaml:bazel:build", "test", 0},
		{"pr.yaml:bazel:build", "baz", 0},
		// Partial segment should NOT match
		{"pr.yaml:bazel:build", "aze", 0},
		{"pr.yaml:bazel:build", "bui", 0},
	}

	for _, tt := range tests {
		got := matchJobKey(tt.jobKey, tt.userKey)
		if got != tt.want {
			t.Errorf("matchJobKey(%q, %q) = %d, want %d", tt.jobKey, tt.userKey, got, tt.want)
		}
	}
}

func TestFindJob_ExactMatch(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{Jobs: []*civ1.JobStatus{
				{JobId: "job-1", JobKey: "build", Status: "running"},
			}},
		},
	}

	job, err := findJob(resp, "build", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-1" {
		t.Fatalf("expected job ID %q, got %q", "job-1", job.JobId)
	}
}

func TestFindJob_SuffixMatch(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{Jobs: []*civ1.JobStatus{
				{JobId: "job-1", JobKey: "_inline_0.yaml:lint_typecheck", Status: "running"},
			}},
		},
	}

	job, err := findJob(resp, "lint_typecheck", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-1" {
		t.Fatalf("expected job ID %q, got %q", "job-1", job.JobId)
	}
}

func TestFindJob_SegmentMatch_ReusableWorkflow(t *testing.T) {
	// This is the Unkey scenario: pr.yaml:bazel:build or _inline_0.yaml:bazel:build
	// where user passes --job=bazel
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{Jobs: []*civ1.JobStatus{
				{JobId: "job-1", JobKey: "_inline_0.yaml:bazel:build", Status: "running"},
			}},
		},
	}

	job, err := findJob(resp, "bazel", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-1" {
		t.Fatalf("expected job ID %q, got %q", "job-1", job.JobId)
	}
}

func TestFindJob_SegmentMatch_WebhookKey(t *testing.T) {
	// Webhook-triggered runs use the workflow filename as prefix
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{Jobs: []*civ1.JobStatus{
				{JobId: "job-1", JobKey: "pr.yaml:bazel:build", Status: "running"},
			}},
		},
	}

	job, err := findJob(resp, "bazel", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-1" {
		t.Fatalf("expected job ID %q, got %q", "job-1", job.JobId)
	}
}

func TestFindJob_SuffixPreferredOverSegment(t *testing.T) {
	// When both suffix and segment would match different jobs, suffix wins
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{Jobs: []*civ1.JobStatus{
				{JobId: "job-segment", JobKey: "pr.yaml:build:test", Status: "running"},
				{JobId: "job-suffix", JobKey: "_inline_0.yaml:build", Status: "running"},
			}},
		},
	}

	// "build" suffix-matches "_inline_0.yaml:build" and segment-matches "pr.yaml:build:test"
	// Suffix should win (more specific)
	job, err := findJob(resp, "build", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-suffix" {
		t.Fatalf("expected suffix match (job-suffix), got %q", job.JobId)
	}
}

func TestFindJob_SegmentMatch_Ambiguous(t *testing.T) {
	// Parent job "build-and-test-backend" expands into two nested jobs.
	// Should auto-select the first match (not error).
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{Jobs: []*civ1.JobStatus{
				{JobId: "job-1", JobKey: "ci.yml:build-and-test-backend:build-backend", Status: "running"},
				{JobId: "job-2", JobKey: "ci.yml:build-and-test-backend:test-backend", Status: "running"},
			}},
		},
	}

	job, err := findJob(resp, "build-and-test-backend", "")
	if err != nil {
		t.Fatalf("expected auto-select first match, got error: %v", err)
	}
	if job.JobId != "job-1" {
		t.Fatalf("expected first match (job-1), got %q", job.JobId)
	}
}

func TestFindJob_AutoSelect(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{Jobs: []*civ1.JobStatus{
				{JobId: "job-1", JobKey: "build", Status: "running"},
			}},
		},
	}

	job, err := findJob(resp, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.JobId != "job-1" {
		t.Fatalf("expected job ID %q, got %q", "job-1", job.JobId)
	}
}

func TestFindJob_NoJobs(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{},
	}

	_, err := findJob(resp, "build", "")
	if err == nil {
		t.Fatal("expected error for no jobs")
	}
	if !isRetryableJobError(err) {
		t.Fatalf("expected retryable error, got: %v", err)
	}
}

func TestFindJob_NotFoundRetryable(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{Jobs: []*civ1.JobStatus{
				{JobId: "job-1", JobKey: "build", Status: "running"},
			}},
		},
	}

	_, err := findJob(resp, "nonexistent", "")
	if err == nil {
		t.Fatal("expected error for non-matching job key")
	}
	if !isRetryableJobError(err) {
		t.Fatalf("expected retryable error, got: %v", err)
	}
}

func TestFindJob_MultipleJobsRequiresFlag(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{Jobs: []*civ1.JobStatus{
				{JobId: "job-1", JobKey: "build", Status: "running"},
				{JobId: "job-2", JobKey: "test", Status: "running"},
			}},
		},
	}

	_, err := findJob(resp, "", "")
	if err == nil {
		t.Fatal("expected error for multiple jobs without --job flag")
	}
}
