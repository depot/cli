package ci

import (
	"context"
	"io"
	"strings"
	"testing"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func TestStatusHumanOutputShowsDownloadForFinishedAttemptsOnly(t *testing.T) {
	originalGetRunStatus := ciGetRunStatus
	t.Cleanup(func() { ciGetRunStatus = originalGetRunStatus })

	var capturedToken string
	var capturedOrgID string
	var capturedRunID string
	ciGetRunStatus = func(ctx context.Context, token, orgID, runID string) (*civ1.GetRunStatusResponse, error) {
		capturedToken = token
		capturedOrgID = orgID
		capturedRunID = runID
		return &civ1.GetRunStatusResponse{
			OrgId:  "org-123",
			RunId:  "run-1",
			Status: "running",
			Workflows: []*civ1.WorkflowStatus{
				{
					WorkflowId:   "workflow-1",
					Status:       "running",
					WorkflowPath: ".depot/workflows/ci.yml",
					Jobs: []*civ1.JobStatus{
						{
							JobId:  "job-1",
							JobKey: "ci.yml:build",
							Status: "running",
							Attempts: []*civ1.AttemptStatus{
								{AttemptId: "att-finished", Attempt: 1, Status: "finished"},
								{AttemptId: "att-running", Attempt: 2, Status: "running", SandboxId: "sandbox-1"},
								{AttemptId: "att-failed", Attempt: 3, Status: "failed"},
							},
						},
					},
				},
			},
		}, nil
	}

	cmd := NewCmdStatus()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "run-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}

	if capturedToken != "token-123" {
		t.Fatalf("token = %q, want token-123", capturedToken)
	}
	if capturedOrgID != "org-123" {
		t.Fatalf("orgID = %q, want org-123", capturedOrgID)
	}
	if capturedRunID != "run-1" {
		t.Fatalf("runID = %q, want run-1", capturedRunID)
	}

	for _, want := range []string{
		"Logs: depot ci logs att-finished --org org-123",
		"Download: depot ci logs att-finished --output-file logs.txt --org org-123",
		"Logs: depot ci logs att-running --org org-123",
		"Logs: depot ci logs att-failed --org org-123",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status output missing %q:\n%s", want, stdout)
		}
	}
	for _, notWant := range []string{
		"Download: depot ci logs att-running",
		"Download: depot ci logs att-failed",
	} {
		if strings.Contains(stdout, notWant) {
			t.Fatalf("status output advertised download for non-finished attempt %q:\n%s", notWant, stdout)
		}
	}
}
