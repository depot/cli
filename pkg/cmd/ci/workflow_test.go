package ci

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	cmd.SetArgs([]string{"--org", "org-123", "-n", "5"})
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

	ciListWorkflows = func(ctx context.Context, token, orgID string, options api.CIListWorkflowsOptions) ([]*civ1.ListWorkflowsResponseWorkflow, error) {
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
	cmd.SetArgs([]string{"--org", "org-123", "--output", "json"})
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

func TestWorkflowListRejectsInvalidLimitBeforeCallingAPI(t *testing.T) {
	originalCIListWorkflows := ciListWorkflows
	t.Cleanup(func() { ciListWorkflows = originalCIListWorkflows })

	called := false
	ciListWorkflows = func(ctx context.Context, token, orgID string, options api.CIListWorkflowsOptions) ([]*civ1.ListWorkflowsResponseWorkflow, error) {
		called = true
		return nil, nil
	}

	for _, limit := range []string{"0", "-1"} {
		cmd := NewCmdWorkflowList()
		cmd.SetArgs([]string{"--org", "org-123", "-n", limit})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)

		_, err := captureStdout(t, cmd.Execute)
		if err == nil || !strings.Contains(err.Error(), "page size (-n) must be greater than 0") {
			t.Fatalf("limit %s error = %v, want page size validation", limit, err)
		}
	}
	if called {
		t.Fatal("ciListWorkflows was called for invalid limit")
	}
}

func TestWorkflowListEmptyResults(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "token-from-env")

	originalCIListWorkflows := ciListWorkflows
	t.Cleanup(func() { ciListWorkflows = originalCIListWorkflows })

	ciListWorkflows = func(ctx context.Context, token, orgID string, options api.CIListWorkflowsOptions) ([]*civ1.ListWorkflowsResponseWorkflow, error) {
		return nil, nil
	}

	cmd := NewCmdWorkflowList()
	cmd.SetArgs([]string{"--org", "org-123"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != "No workflows found." {
		t.Fatalf("stdout = %q, want empty-results message", stdout)
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
	for _, existing := range [][]string{{"run", "list"}, {"status"}, {"logs"}} {
		if findCommand(t, cmd, existing...) == nil {
			t.Fatalf("depot ci %s is not registered", strings.Join(existing, " "))
		}
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
