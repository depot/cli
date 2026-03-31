package ci

import (
	"testing"

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

func TestResolveAttempt_LatestAttempt(t *testing.T) {
	resp := &civ1.GetRunStatusResponse{
		RunId: "run-1",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{
						JobId:  "job-1",
						JobKey: "build",
						Status: "finished",
						Attempts: []*civ1.AttemptStatus{
							{AttemptId: "att-1", Attempt: 1, Status: "failed"},
							{AttemptId: "att-2", Attempt: 2, Status: "finished"},
						},
					},
				},
			},
		},
	}

	attemptID, err := resolveAttempt(resp, "run-1", "build", "")
	if err != nil {
		t.Fatal(err)
	}
	if attemptID != "att-2" {
		t.Fatalf("expected attempt ID %q, got %q", "att-2", attemptID)
	}
}

func TestResolveAttempt_NoAttempts(t *testing.T) {
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

	_, err := resolveAttempt(resp, "run-1", "", "")
	if err == nil {
		t.Fatal("expected error for job with no attempts")
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
