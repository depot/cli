package ci

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/depot/cli/pkg/api"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	originalStdout := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writePipe
	defer func() { os.Stdout = originalStdout }()

	runErr := fn()

	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}

	out, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
}

func TestRunListPassesRepoShaBranchAndPullRequestFilters(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "token-from-env")

	originalCIListRuns := ciListRuns
	t.Cleanup(func() { ciListRuns = originalCIListRuns })

	var capturedToken string
	var capturedOrgID string
	var capturedOptions api.CIListRunsOptions
	ciListRuns = func(ctx context.Context, token, orgID string, options api.CIListRunsOptions) ([]*civ1.ListRunsResponseRun, error) {
		capturedToken = token
		capturedOrgID = orgID
		capturedOptions = options
		return []*civ1.ListRunsResponseRun{
			{
				RunId:     "run-1",
				Repo:      "depot/api",
				Ref:       "refs/heads/main",
				Sha:       "merge123",
				HeadSha:   "head456",
				Status:    "failed",
				Trigger:   "push",
				CreatedAt: "2026-04-28T12:00:00Z",
			},
		}, nil
	}

	cmd := NewCmdRunList()
	cmd.SetArgs([]string{
		"--org", "org-123",
		"--repo", "depot/api",
		"--sha", "ABC123",
		"--branch", "main",
		"--pr", "42",
		"--status", "failed",
		"--output", "json",
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
	if capturedOptions.Repo != "depot/api" {
		t.Fatalf("Repo = %q, want depot/api", capturedOptions.Repo)
	}
	if capturedOptions.Sha != "ABC123" {
		t.Fatalf("Sha = %q, want ABC123", capturedOptions.Sha)
	}
	if capturedOptions.Branch != "main" {
		t.Fatalf("Branch = %q, want main", capturedOptions.Branch)
	}
	if capturedOptions.Ref != "" {
		t.Fatalf("Ref = %q, want empty", capturedOptions.Ref)
	}
	if capturedOptions.PullRequestNumber != 42 {
		t.Fatalf("PullRequestNumber = %d, want 42", capturedOptions.PullRequestNumber)
	}
	if capturedOptions.Limit != 50 {
		t.Fatalf("Limit = %d, want 50", capturedOptions.Limit)
	}
	if len(capturedOptions.Statuses) != 1 || capturedOptions.Statuses[0] != civ1.CIRunStatus_CI_RUN_STATUS_FAILED {
		t.Fatalf("Statuses = %v, want [FAILED]", capturedOptions.Statuses)
	}
	if !strings.Contains(stdout, `"ref": "refs/heads/main"`) {
		t.Fatalf("JSON output missing ref field:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"head_sha": "head456"`) {
		t.Fatalf("JSON output missing head_sha field:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"status": "failed"`) {
		t.Fatalf("JSON output missing status field:\n%s", stdout)
	}
}

func TestRunListRejectsInvalidPullRequestNumber(t *testing.T) {
	originalCIListRuns := ciListRuns
	t.Cleanup(func() { ciListRuns = originalCIListRuns })

	ciListRuns = func(ctx context.Context, token, orgID string, options api.CIListRunsOptions) ([]*civ1.ListRunsResponseRun, error) {
		t.Fatal("ciListRuns should not be called")
		return nil, nil
	}

	cmd := NewCmdRunList()
	cmd.SetArgs([]string{
		"--repo", "depot/api",
		"--pr", "0",
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid pull request number error")
	}
	if !strings.Contains(err.Error(), "--pr must be greater than 0") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunListRejectsPullRequestWithoutRepo(t *testing.T) {
	originalCIListRuns := ciListRuns
	t.Cleanup(func() { ciListRuns = originalCIListRuns })

	ciListRuns = func(ctx context.Context, token, orgID string, options api.CIListRunsOptions) ([]*civ1.ListRunsResponseRun, error) {
		t.Fatal("ciListRuns should not be called")
		return nil, nil
	}

	cmd := NewCmdRunList()
	cmd.SetArgs([]string{
		"--pr", "42",
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected missing repo error")
	}
	if !strings.Contains(err.Error(), "--repo is required when using --pr") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunListPassesQualifiedRefFilter(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "token-from-env")

	originalCIListRuns := ciListRuns
	t.Cleanup(func() { ciListRuns = originalCIListRuns })

	var capturedOptions api.CIListRunsOptions
	ciListRuns = func(ctx context.Context, token, orgID string, options api.CIListRunsOptions) ([]*civ1.ListRunsResponseRun, error) {
		capturedOptions = options
		return nil, nil
	}

	cmd := NewCmdRunList()
	cmd.SetArgs([]string{
		"--org", "org-123",
		"--ref", "refs/heads/feature/x",
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if capturedOptions.Ref != "refs/heads/feature/x" {
		t.Fatalf("Ref = %q, want refs/heads/feature/x", capturedOptions.Ref)
	}
}

func TestRunListRejectsBranchAndRefTogether(t *testing.T) {
	originalCIListRuns := ciListRuns
	t.Cleanup(func() { ciListRuns = originalCIListRuns })

	ciListRuns = func(ctx context.Context, token, orgID string, options api.CIListRunsOptions) ([]*civ1.ListRunsResponseRun, error) {
		t.Fatal("ciListRuns should not be called")
		return nil, nil
	}

	cmd := NewCmdRunList()
	cmd.SetArgs([]string{
		"--branch", "main",
		"--ref", "refs/heads/main",
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected conflicting branch/ref error")
	}
	if !strings.Contains(err.Error(), "--branch and --ref are mutually exclusive") {
		t.Fatalf("error = %v", err)
	}
}
