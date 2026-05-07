package ci

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func testStatusResponse() *civ1.GetRunStatusResponse {
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
							{AttemptId: "att-running", Attempt: 2, Status: "running", SandboxId: "sandbox-1", SessionId: "session-1"},
							{AttemptId: "att-failed", Attempt: 3, Status: "failed"},
						},
					},
				},
			},
		},
	}
}

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
		return testStatusResponse(), nil
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
		"View: https://depot.dev/orgs/org-123/workflows/workflow-1?job=job-1&attempt=att-finished",
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

func TestStatusJSONOutput(t *testing.T) {
	originalGetRunStatus := ciGetRunStatus
	t.Cleanup(func() { ciGetRunStatus = originalGetRunStatus })

	ciGetRunStatus = func(ctx context.Context, token, orgID, runID string) (*civ1.GetRunStatusResponse, error) {
		return testStatusResponse(), nil
	}

	cmd := NewCmdStatus()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output", "json", "run-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "Logs: depot ci logs") {
		t.Fatalf("json output included human log hints:\n%s", stdout)
	}

	var got struct {
		OrgID     string `json:"org_id"`
		RunID     string `json:"run_id"`
		Status    string `json:"status"`
		Workflows []struct {
			WorkflowID   string `json:"workflow_id"`
			WorkflowPath string `json:"workflow_path"`
			Jobs         []struct {
				JobID    string `json:"job_id"`
				JobKey   string `json:"job_key"`
				Attempts []struct {
					AttemptID         string `json:"attempt_id"`
					Status            string `json:"status"`
					SandboxID         string `json:"sandbox_id"`
					SessionID         string `json:"session_id"`
					LogsCommand       string `json:"logs_command"`
					DownloadAvailable bool   `json:"download_available"`
					DownloadCommand   string `json:"download_command"`
					ViewURL           string `json:"view_url"`
					SSHAvailable      bool   `json:"ssh_available"`
					SSHCommand        string `json:"ssh_command"`
				} `json:"attempts"`
			} `json:"jobs"`
		} `json:"workflows"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	if got.OrgID != "org-123" || got.RunID != "run-1" || got.Status != "running" {
		t.Fatalf("unexpected run JSON: %+v", got)
	}
	if len(got.Workflows) != 1 || got.Workflows[0].WorkflowID != "workflow-1" || got.Workflows[0].WorkflowPath != ".depot/workflows/ci.yml" {
		t.Fatalf("unexpected workflow JSON: %+v", got.Workflows)
	}
	if len(got.Workflows[0].Jobs) != 1 || got.Workflows[0].Jobs[0].JobID != "job-1" || got.Workflows[0].Jobs[0].JobKey != "ci.yml:build" {
		t.Fatalf("unexpected job JSON: %+v", got.Workflows[0].Jobs)
	}
	attempts := got.Workflows[0].Jobs[0].Attempts
	if len(attempts) != 3 || attempts[1].AttemptID != "att-running" || attempts[1].SandboxID != "sandbox-1" || attempts[1].SessionID != "session-1" {
		t.Fatalf("unexpected attempts JSON: %+v", attempts)
	}
	if attempts[0].LogsCommand != "depot ci logs att-finished --org org-123" {
		t.Fatalf("finished logs command = %q", attempts[0].LogsCommand)
	}
	if !attempts[0].DownloadAvailable || attempts[0].DownloadCommand != "depot ci logs att-finished --output-file logs.txt --org org-123" {
		t.Fatalf("unexpected finished download affordance: %+v", attempts[0])
	}
	if attempts[0].ViewURL != "https://depot.dev/orgs/org-123/workflows/workflow-1?job=job-1&attempt=att-finished" {
		t.Fatalf("finished view url = %q", attempts[0].ViewURL)
	}
	if !attempts[1].SSHAvailable || attempts[1].SSHCommand != "depot ci ssh run-1 --job ci.yml:build --org org-123" {
		t.Fatalf("unexpected running ssh affordance: %+v", attempts[1])
	}
	if attempts[1].DownloadAvailable || attempts[1].DownloadCommand != "" {
		t.Fatalf("running attempt should not expose download affordance: %+v", attempts[1])
	}
	if attempts[2].DownloadAvailable || attempts[2].SSHAvailable || attempts[2].DownloadCommand != "" || attempts[2].SSHCommand != "" {
		t.Fatalf("failed attempt exposed unavailable affordances: %+v", attempts[2])
	}
}

func TestStatusRejectsUnsupportedOutput(t *testing.T) {
	originalGetRunStatus := ciGetRunStatus
	t.Cleanup(func() { ciGetRunStatus = originalGetRunStatus })

	ciGetRunStatus = func(ctx context.Context, token, orgID, runID string) (*civ1.GetRunStatusResponse, error) {
		t.Fatal("ciGetRunStatus should not be called for invalid output")
		return nil, nil
	}

	cmd := NewCmdStatus()
	cmd.SetArgs([]string{"--token", "token-123", "--output", "yaml", "run-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	_, err := captureStdout(t, cmd.Execute)
	if err == nil || !strings.Contains(err.Error(), `unsupported output "yaml"`) {
		t.Fatalf("expected unsupported output error, got %v", err)
	}
}
