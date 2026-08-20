package ci

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
)

type recordingRepositoryAnalysisClient struct {
	request  *connect.Request[civ2.GetRepositoryMigrationAnalysisRequest]
	response *civ2.GetRepositoryMigrationAnalysisResponse
	err      error
	calls    int
}

func (c *recordingRepositoryAnalysisClient) GetRepositoryAnalysis(
	_ context.Context,
	request *connect.Request[civ2.GetRepositoryMigrationAnalysisRequest],
) (*connect.Response[civ2.GetRepositoryMigrationAnalysisResponse], error) {
	c.calls++
	c.request = request
	if c.err != nil {
		return nil, c.err
	}
	return connect.NewResponse(c.response), nil
}

func TestCursorMigrationAnalyzesSelectedLocalWorkflowsBeforeMigrating(t *testing.T) {
	dir, workflow := newMigrationTestRepository(t)
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://github.com/acme/widgets.git")
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "cursor", "git@origin.cursor.com:git/acme/widgets.git")

	client := &recordingRepositoryAnalysisClient{response: &civ2.GetRepositoryMigrationAnalysisResponse{
		Workflows: []*civ2.RepositoryWorkflowMigration{{
			Path: ".github/workflows/ci.yml",
			Analysis: &civ2.WorkflowAnalysis{Blockers: []*civ2.MigrationDiagnostic{{
				Code:     "origin.unsupported",
				Severity: civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_FAILS,
			}}},
		}},
	}}
	var output bytes.Buffer
	err := workflowsWithContext(context.Background(), migrateOptions{
		dir:                      dir,
		stdout:                   &output,
		yes:                      true,
		forge:                    migrationForgeCursor,
		token:                    "depot_api_token",
		orgID:                    "org-id",
		repositoryAnalysisClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}

	if client.calls != 1 {
		t.Fatalf("analysis calls = %d, want 1", client.calls)
	}
	request := client.request
	if request.Header().Get("Authorization") != "Bearer depot_api_token" {
		t.Fatalf("unexpected authorization header: %q", request.Header().Get("Authorization"))
	}
	if request.Header().Get("x-depot-org") != "org-id" {
		t.Fatalf("unexpected organization header: %q", request.Header().Get("x-depot-org"))
	}
	if request.Msg.GetForge() != civ2.MigrationForge_MIGRATION_FORGE_CURSOR_ORIGIN {
		t.Fatalf("forge = %s, want Cursor Origin", request.Msg.GetForge())
	}
	if request.Msg.GetRepositoryUrl() != "origin.cursor.com/acme/widgets" {
		t.Fatalf("repository URL = %q", request.Msg.GetRepositoryUrl())
	}
	if request.Msg.GetInstallationId() != "" || request.Msg.GetWorkflowRepoId() != "" {
		t.Fatal("Depot-authenticated analysis must not send caller-supplied installation or repository IDs")
	}
	files := request.Msg.GetWorkflowFiles().GetWorkflows()
	if len(files) != 1 || files[0].GetPath() != ".github/workflows/ci.yml" || files[0].GetContent() != workflow {
		t.Fatalf("unexpected local workflow payload: %#v", files)
	}
	if _, err := os.Stat(filepath.Join(dir, ".depot", "workflows", "ci.yml")); err != nil {
		t.Fatalf("diagnostic findings must not stop migration: %v", err)
	}
}

func TestGitHubMigrationDoesNotRequestOriginAnalysis(t *testing.T) {
	dir, _ := newMigrationTestRepository(t)
	client := &recordingRepositoryAnalysisClient{err: errors.New("must not be called")}
	if err := workflows(migrateOptions{
		dir:                      dir,
		yes:                      true,
		repositoryAnalysisClient: client,
	}); err != nil {
		t.Fatal(err)
	}
	if client.calls != 0 {
		t.Fatalf("Origin analysis calls = %d, want 0", client.calls)
	}
}

func TestCursorMigrationReportsAnalysisFailureBeforeWritingFiles(t *testing.T) {
	dir, _ := newMigrationTestRepository(t)
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://origin.cursor.com/git/acme/widgets")
	client := &recordingRepositoryAnalysisClient{err: connect.NewError(connect.CodePermissionDenied, errors.New("repository is not available to this organization"))}

	err := workflowsWithContext(context.Background(), migrateOptions{
		dir:                      dir,
		yes:                      true,
		forge:                    migrationForgeCursor,
		token:                    "depot_api_token",
		orgID:                    "org-id",
		repositoryAnalysisClient: client,
	})
	if err == nil || !strings.Contains(err.Error(), "failed to analyze Cursor Origin compatibility for origin.cursor.com/acme/widgets") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".depot")); !os.IsNotExist(statErr) {
		t.Fatalf("migration wrote files after analysis failure: %v", statErr)
	}
}

func TestCursorPreflightUsesOriginAnalysisInsteadOfGitHubCodeAccess(t *testing.T) {
	dir, _ := newMigrationTestRepository(t)
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://origin.cursor.com/git/acme/widgets")
	client := &recordingRepositoryAnalysisClient{response: &civ2.GetRepositoryMigrationAnalysisResponse{}}
	var output bytes.Buffer

	result, err := preflight(context.Background(), migrateOptions{
		dir:                      dir,
		stdout:                   &output,
		forge:                    migrationForgeCursor,
		token:                    "depot_org_token",
		repositoryAnalysisClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.repo != "origin.cursor.com/acme/widgets" {
		t.Fatalf("unexpected preflight result: %#v", result)
	}
	if client.calls != 1 || len(client.request.Msg.GetWorkflowFiles().GetWorkflows()) != 0 {
		t.Fatal("Cursor preflight must validate repository access with an empty local analysis request")
	}
	if client.request.Header().Get("Authorization") != "Bearer depot_org_token" || client.request.Header().Get("x-depot-org") != "" {
		t.Fatalf("unexpected organization-token authentication headers: %#v", client.request.Header())
	}
	if !strings.Contains(output.String(), "Cursor Origin repository is available") {
		t.Fatalf("unexpected output: %s", output.String())
	}
}

func TestCursorMigrationReportsMissingOriginRemote(t *testing.T) {
	dir, _ := newMigrationTestRepository(t)
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://github.com/acme/widgets.git")
	client := &recordingRepositoryAnalysisClient{response: &civ2.GetRepositoryMigrationAnalysisResponse{}}

	err := workflowsWithContext(context.Background(), migrateOptions{
		dir:                      dir,
		yes:                      true,
		forge:                    migrationForgeCursor,
		token:                    "depot_org_token",
		repositoryAnalysisClient: client,
	})
	if err == nil || !strings.Contains(err.Error(), "configure a remote pointing to origin.cursor.com/namespace/repo") {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.calls != 0 {
		t.Fatal("analysis request was made without a Cursor Origin remote")
	}
}

func TestCursorMigrationDoesNotUploadWorkflowOutsideRepository(t *testing.T) {
	dir, _ := newMigrationTestRepository(t)
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://origin.cursor.com/git/acme/widgets")
	external := filepath.Join(t.TempDir(), "external.yml")
	if err := os.WriteFile(external, []byte("on: push\n"), 0644); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(dir, ".github", "workflows", "ci.yml")
	if err := os.Remove(workflowPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, workflowPath); err != nil {
		t.Fatal(err)
	}
	client := &recordingRepositoryAnalysisClient{response: &civ2.GetRepositoryMigrationAnalysisResponse{}}

	err := workflowsWithContext(context.Background(), migrateOptions{
		dir:                      dir,
		yes:                      true,
		forge:                    migrationForgeCursor,
		token:                    "depot_api_token",
		orgID:                    "org-id",
		repositoryAnalysisClient: client,
	})
	if err == nil || !strings.Contains(err.Error(), "resolves outside the repository") {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.calls != 0 {
		t.Fatal("analysis client received content from outside the repository")
	}
}

func TestMigrateForgeFlagRejectsUnsupportedValueOnSubcommands(t *testing.T) {
	cmd := NewCmdMigrate()
	cmd.SetArgs([]string{"workflows", "--forge", "gitlab", "--yes"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), `invalid argument "gitlab" for "--forge" flag`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMigrateForgeFlagAcceptsSupportedValues(t *testing.T) {
	for _, forge := range []string{"github", "cursor"} {
		t.Run(forge, func(t *testing.T) {
			cmd := NewCmdMigrate()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"workflows", "--forge", forge, "--help"})
			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("--forge=%s was rejected: %v", forge, err)
			}
		})
	}
}

func newMigrationTestRepository(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runSecretMigrationTestCommand(t, "", "git", "init", dir)
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatal(err)
	}
	workflow := "name: CI\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n"
	if err := os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte(workflow), 0644); err != nil {
		t.Fatal(err)
	}
	return dir, workflow
}
