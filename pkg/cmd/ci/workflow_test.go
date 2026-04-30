package ci

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/depot/cli/pkg/api"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

func TestWorkflowListPassesRequestOptionsAndPrintsTable(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "token-from-env")

	originalCIListWorkflows := ciListWorkflows
	t.Cleanup(func() { ciListWorkflows = originalCIListWorkflows })

	var capturedToken string
	var capturedOrgID string
	var capturedOptions api.CIListWorkflowsOptions
	ciListWorkflows = func(ctx context.Context, token, orgID string, options api.CIListWorkflowsOptions) ([]*civ1.ListWorkflowsResponseWorkflow, error) {
		capturedToken = token
		capturedOrgID = orgID
		capturedOptions = options
		return []*civ1.ListWorkflowsResponseWorkflow{
			{
				WorkflowId:   "workflow-1",
				Name:         "CI",
				WorkflowPath: ".depot/workflows/ci.yml",
				Repo:         "depot/api",
				Status:       "failed",
				Trigger:      "push",
				RunId:        "run-1",
				Sha:          "merge123",
				HeadSha:      "head456",
				CreatedAt:    "2026-04-28T12:00:00Z",
				JobCounts:    &civ1.ListWorkflowsResponseJobCounts{Total: 2, Failed: 1, Finished: 1},
			},
		}, nil
	}

	cmd := NewCmdWorkflowList()
	cmd.SetArgs([]string{
		"--org",
		"org-123",
		"-n",
		"5",
		"--name",
		"deploy",
		"--repo",
		"depot/api",
		"--status",
		"running",
		"--status",
		"failed",
		"--trigger",
		"workflow_dispatch",
		"--sha",
		"abc123",
		"--pr",
		"42",
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}

	if capturedToken != "token-from-env" {
		t.Fatalf("token = %q, want token-from-env", capturedToken)
	}
	if capturedOrgID != "org-123" {
		t.Fatalf("orgID = %q, want org-123", capturedOrgID)
	}
	if capturedOptions.Limit != 5 {
		t.Fatalf("Limit = %d, want 5", capturedOptions.Limit)
	}
	if capturedOptions.Name != "deploy" {
		t.Fatalf("Name = %q, want deploy", capturedOptions.Name)
	}
	if capturedOptions.Repo != "depot/api" {
		t.Fatalf("Repo = %q, want depot/api", capturedOptions.Repo)
	}
	if got, want := capturedOptions.Statuses, []string{"running", "failed"}; !slices.Equal(got, want) {
		t.Fatalf("Statuses = %v, want %v", got, want)
	}
	if capturedOptions.Trigger != "workflow_dispatch" {
		t.Fatalf("Trigger = %q, want workflow_dispatch", capturedOptions.Trigger)
	}
	if capturedOptions.Sha != "abc123" {
		t.Fatalf("Sha = %q, want abc123", capturedOptions.Sha)
	}
	if capturedOptions.PullRequest != "42" {
		t.Fatalf("PullRequest = %q, want 42", capturedOptions.PullRequest)
	}
	for _, want := range []string{"WORKFLOW ID", "workflow-1", "CI", "depot/api", "failed", "push", "head456", "run-1"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("table output missing %q:\n%s", want, stdout)
		}
	}
}

func TestWorkflowListJSONOutput(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "token-from-env")

	originalCIListWorkflows := ciListWorkflows
	t.Cleanup(func() { ciListWorkflows = originalCIListWorkflows })

	var capturedOptions api.CIListWorkflowsOptions
	ciListWorkflows = func(ctx context.Context, token, orgID string, options api.CIListWorkflowsOptions) ([]*civ1.ListWorkflowsResponseWorkflow, error) {
		capturedOptions = options
		return []*civ1.ListWorkflowsResponseWorkflow{
			{
				WorkflowId:   "workflow-1",
				Name:         "CI",
				WorkflowPath: ".depot/workflows/ci.yml",
				Repo:         "depot/api",
				Status:       "finished",
				Trigger:      "workflow_dispatch",
				RunId:        "run-1",
				Sha:          "merge123",
				HeadSha:      "head456",
				CreatedAt:    "2026-04-28T12:00:00Z",
				JobCounts: &civ1.ListWorkflowsResponseJobCounts{
					Total:     8,
					Queued:    1,
					Waiting:   2,
					Running:   3,
					Finished:  4,
					Failed:    5,
					Cancelled: 6,
					Skipped:   7,
				},
			},
		}, nil
	}

	cmd := NewCmdWorkflowList()
	cmd.SetArgs([]string{"--org", "org-123", "--output", "json", "--name", "workflow_dispatch"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}

	var workflows []workflowListJSON
	if err := json.Unmarshal([]byte(stdout), &workflows); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(workflows))
	}
	if capturedOptions.Name != "workflow_dispatch" {
		t.Fatalf("Name = %q, want workflow_dispatch", capturedOptions.Name)
	}

	got := workflows[0]
	if got.WorkflowID != "workflow-1" ||
		got.Name != "CI" ||
		got.WorkflowPath != ".depot/workflows/ci.yml" ||
		got.Repo != "depot/api" ||
		got.Status != "finished" ||
		got.Trigger != "workflow_dispatch" ||
		got.RunID != "run-1" ||
		got.Sha != "merge123" ||
		got.HeadSha != "head456" ||
		got.CreatedAt != "2026-04-28T12:00:00Z" {
		t.Fatalf("unexpected workflow JSON: %+v", got)
	}
	if got.JobCounts.Total != 8 ||
		got.JobCounts.Queued != 1 ||
		got.JobCounts.Waiting != 2 ||
		got.JobCounts.Running != 3 ||
		got.JobCounts.Finished != 4 ||
		got.JobCounts.Failed != 5 ||
		got.JobCounts.Cancelled != 6 ||
		got.JobCounts.Skipped != 7 {
		t.Fatalf("unexpected job counts JSON: %+v", got.JobCounts)
	}
	if !strings.Contains(stdout, `"workflow_id": "workflow-1"`) {
		t.Fatalf("JSON output should use stable snake_case fields:\n%s", stdout)
	}
}

func TestWorkflowShowPassesWorkflowIDAndPrintsDetails(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "token-from-env")

	originalCIGetWorkflow := ciGetWorkflow
	t.Cleanup(func() { ciGetWorkflow = originalCIGetWorkflow })

	var capturedToken string
	var capturedOrgID string
	var capturedWorkflowID string
	ciGetWorkflow = func(ctx context.Context, token, orgID, workflowID string) (*civ1.GetWorkflowResponse, error) {
		capturedToken = token
		capturedOrgID = orgID
		capturedWorkflowID = workflowID
		return sampleGetWorkflowResponse(), nil
	}

	cmd := NewCmdWorkflowShow()
	cmd.SetArgs([]string{"--org", "org-123", "workflow-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}

	if capturedToken != "token-from-env" {
		t.Fatalf("token = %q, want token-from-env", capturedToken)
	}
	if capturedOrgID != "org-123" {
		t.Fatalf("orgID = %q, want org-123", capturedOrgID)
	}
	if capturedWorkflowID != "workflow-1" {
		t.Fatalf("workflowID = %q, want workflow-1", capturedWorkflowID)
	}

	for _, want := range []string{
		"Org: org-123",
		"Repo: depot/api",
		"Run: run-1 (failed)",
		"Workflow: workflow-1 (failed)",
		"Name: CI",
		"Path: .depot/workflows/ci.yml",
		"#1 finished 8m42s",
		"#2 failed 3m14s",
		"build [finished] 4m2s",
		"test [failed] 2m58s",
		"Latest attempt: #2 att-test-2 (failed) 2m58s",
		"Sandbox: sandbox-2",
		"Session: session-2",
		"Logs: depot ci logs att-test-2 --org org-123",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("workflow show output missing %q:\n%s", want, stdout)
		}
	}
}

func TestWorkflowShowJSONOutput(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "token-from-env")

	originalCIGetWorkflow := ciGetWorkflow
	t.Cleanup(func() { ciGetWorkflow = originalCIGetWorkflow })

	ciGetWorkflow = func(ctx context.Context, token, orgID, workflowID string) (*civ1.GetWorkflowResponse, error) {
		return sampleGetWorkflowResponse(), nil
	}

	cmd := NewCmdWorkflowShow()
	cmd.SetArgs([]string{"--org", "org-123", "--output", "json", "workflow-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}

	var workflow workflowShowJSON
	if err := json.Unmarshal([]byte(stdout), &workflow); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	if workflow.OrgID != "org-123" {
		t.Fatalf("org_id = %q, want org-123", workflow.OrgID)
	}
	if workflow.Run.RunID != "run-1" || workflow.Run.Status != "failed" {
		t.Fatalf("unexpected run JSON: %+v", workflow.Run)
	}
	if workflow.Workflow.WorkflowID != "workflow-1" || workflow.Workflow.WorkflowPath != ".depot/workflows/ci.yml" {
		t.Fatalf("unexpected workflow JSON: %+v", workflow.Workflow)
	}
	if len(workflow.Executions) != 2 || workflow.Executions[1].ExecutionID != "exec-2" {
		t.Fatalf("unexpected executions JSON: %+v", workflow.Executions)
	}
	if len(workflow.Jobs) != 2 || len(workflow.Jobs[1].Attempts) != 2 {
		t.Fatalf("unexpected jobs JSON: %+v", workflow.Jobs)
	}
	if workflow.Jobs[1].Attempts[1].SessionID != "session-2" {
		t.Fatalf("unexpected attempt JSON: %+v", workflow.Jobs[1].Attempts[1])
	}
}

func TestWorkflowListRejectsInvalidLimitBeforeCallingAPI(t *testing.T) {
	originalCIListWorkflows := ciListWorkflows
	t.Cleanup(func() { ciListWorkflows = originalCIListWorkflows })

	called := false
	ciListWorkflows = func(ctx context.Context, token, orgID string, options api.CIListWorkflowsOptions) ([]*civ1.ListWorkflowsResponseWorkflow, error) {
		called = true
		return nil, nil
	}

	for _, tt := range []struct {
		limit string
		want  string
	}{
		{limit: "0", want: "page size (-n) must be greater than 0"},
		{limit: "-1", want: "page size (-n) must be greater than 0"},
		{limit: "201", want: "page size (-n) must be 200 or less"},
	} {
		cmd := NewCmdWorkflowList()
		cmd.SetArgs([]string{"--org", "org-123", "-n", tt.limit})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)

		_, err := captureStdout(t, cmd.Execute)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("limit %s error = %v, want %q", tt.limit, err, tt.want)
		}
	}
	if called {
		t.Fatal("ciListWorkflows was called for invalid limit")
	}
}

func TestWorkflowListRejectsInvalidStatusBeforeCallingAPI(t *testing.T) {
	originalCIListWorkflows := ciListWorkflows
	t.Cleanup(func() { ciListWorkflows = originalCIListWorkflows })

	called := false
	ciListWorkflows = func(ctx context.Context, token, orgID string, options api.CIListWorkflowsOptions) ([]*civ1.ListWorkflowsResponseWorkflow, error) {
		called = true
		return nil, nil
	}

	cmd := NewCmdWorkflowList()
	cmd.SetArgs([]string{"--org", "org-123", "--status", "skipped"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	_, err := captureStdout(t, cmd.Execute)
	if err == nil || !strings.Contains(err.Error(), `invalid status "skipped"`) {
		t.Fatalf("error = %v, want invalid status validation", err)
	}
	if called {
		t.Fatal("ciListWorkflows was called for invalid status")
	}
}

func TestWorkflowListEmptyResults(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "token-from-env")

	originalCIListWorkflows := ciListWorkflows
	t.Cleanup(func() { ciListWorkflows = originalCIListWorkflows })

	var capturedOptions api.CIListWorkflowsOptions
	ciListWorkflows = func(ctx context.Context, token, orgID string, options api.CIListWorkflowsOptions) ([]*civ1.ListWorkflowsResponseWorkflow, error) {
		capturedOptions = options
		return nil, nil
	}

	cmd := NewCmdWorkflowList()
	cmd.SetArgs([]string{"--org", "org-123", "--name", "no-matches"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != "No workflows found." {
		t.Fatalf("stdout = %q, want empty-results message", stdout)
	}
	if capturedOptions.Name != "no-matches" {
		t.Fatalf("Name = %q, want no-matches", capturedOptions.Name)
	}
}

func TestWorkflowListWrapsAPIErrors(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "token-from-env")

	originalCIListWorkflows := ciListWorkflows
	t.Cleanup(func() { ciListWorkflows = originalCIListWorkflows })

	ciListWorkflows = func(ctx context.Context, token, orgID string, options api.CIListWorkflowsOptions) ([]*civ1.ListWorkflowsResponseWorkflow, error) {
		return nil, errors.New("server unavailable")
	}

	cmd := NewCmdWorkflowList()
	cmd.SetArgs([]string{"--org", "org-123"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	_, err := captureStdout(t, cmd.Execute)
	if err == nil || !strings.Contains(err.Error(), "failed to list workflows: server unavailable") {
		t.Fatalf("error = %v, want wrapped API error", err)
	}
}

func TestCICommandRegistersWorkflowListWithoutBreakingExistingCommands(t *testing.T) {
	cmd := NewCmdCI()

	if findCommand(t, cmd, "workflow", "list") == nil {
		t.Fatal("depot ci workflow list is not registered")
	}
	if findCommand(t, cmd, "workflow", "show") == nil {
		t.Fatal("depot ci workflow show is not registered")
	}
	for _, existing := range [][]string{{"run", "list"}, {"status"}, {"logs"}} {
		if findCommand(t, cmd, existing...) == nil {
			t.Fatalf("depot ci %s is not registered", strings.Join(existing, " "))
		}
	}
}

func sampleGetWorkflowResponse() *civ1.GetWorkflowResponse {
	return &civ1.GetWorkflowResponse{
		OrgId:                "org-123",
		RunId:                "run-1",
		Repo:                 "depot/api",
		Ref:                  "refs/heads/main",
		Sha:                  "merge123",
		HeadSha:              "head456",
		Trigger:              "push",
		RunStatus:            "failed",
		RunCreatedAt:         "2026-04-30T14:01:00Z",
		RunStartedAt:         "2026-04-30T14:02:00Z",
		RunFinishedAt:        "2026-04-30T14:25:15Z",
		WorkflowId:           "workflow-1",
		WorkflowName:         "CI",
		WorkflowPath:         ".depot/workflows/ci.yml",
		WorkflowStatus:       "failed",
		WorkflowErrorMessage: "tests failed",
		WorkflowCreatedAt:    "2026-04-30T14:01:05Z",
		WorkflowStartedAt:    "2026-04-30T14:02:11Z",
		WorkflowFinishedAt:   "2026-04-30T14:25:15Z",
		Executions: []*civ1.GetWorkflowExecution{
			{
				ExecutionId: "exec-1",
				Execution:   1,
				Status:      "finished",
				CreatedAt:   "2026-04-30T14:01:05Z",
				StartedAt:   "2026-04-30T14:02:11Z",
				FinishedAt:  "2026-04-30T14:10:53Z",
			},
			{
				ExecutionId: "exec-2",
				Execution:   2,
				Status:      "failed",
				CreatedAt:   "2026-04-30T14:21:45Z",
				StartedAt:   "2026-04-30T14:22:01Z",
				FinishedAt:  "2026-04-30T14:25:15Z",
			},
		},
		Jobs: []*civ1.GetWorkflowJob{
			{
				JobId:      "job-build",
				JobKey:     "ci.yml:build",
				Status:     "finished",
				StartedAt:  "2026-04-30T14:02:20Z",
				FinishedAt: "2026-04-30T14:06:22Z",
				Attempts: []*civ1.GetWorkflowJobAttempt{
					{
						AttemptId:  "att-build-1",
						Attempt:    1,
						Status:     "finished",
						StartedAt:  "2026-04-30T14:02:20Z",
						FinishedAt: "2026-04-30T14:06:22Z",
					},
				},
			},
			{
				JobId:      "job-test",
				JobKey:     "ci.yml:test",
				Status:     "failed",
				StartedAt:  "2026-04-30T14:22:10Z",
				FinishedAt: "2026-04-30T14:25:08Z",
				Attempts: []*civ1.GetWorkflowJobAttempt{
					{
						AttemptId:  "att-test-1",
						Attempt:    1,
						Status:     "failed",
						StartedAt:  "2026-04-30T14:07:00Z",
						FinishedAt: "2026-04-30T14:09:10Z",
					},
					{
						AttemptId:  "att-test-2",
						Attempt:    2,
						Status:     "failed",
						SandboxId:  "sandbox-2",
						SessionId:  "session-2",
						StartedAt:  "2026-04-30T14:22:10Z",
						FinishedAt: "2026-04-30T14:25:08Z",
					},
				},
			},
		},
	}
}

func findCommand(t *testing.T, cmd *cobra.Command, path ...string) *cobra.Command {
	t.Helper()

	current := cmd
	for _, name := range path {
		next, _, err := current.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", strings.Join(path, " "), err)
		}
		if next == nil || next == current {
			return nil
		}
		current = next
	}
	return current
}
