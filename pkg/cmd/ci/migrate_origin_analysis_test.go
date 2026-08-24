package ci

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type recordingRepositoryAnalysisClient struct {
	request  *connect.Request[civ2.GetRepositoryMigrationAnalysisRequest]
	response *civ2.GetRepositoryMigrationAnalysisResponse
	err      error
	calls    int
}

type recordingGitHubMigrationService struct {
	civ1connect.UnimplementedMigrationServiceHandler
	requests []*connect.Request[civ1.GetInstallationRequest]
}

func (s *recordingGitHubMigrationService) GetInstallation(
	_ context.Context,
	request *connect.Request[civ1.GetInstallationRequest],
) (*connect.Response[civ1.GetInstallationResponse], error) {
	s.requests = append(s.requests, request)
	return connect.NewResponse(&civ1.GetInstallationResponse{
		Installations: []*civ1.Installation{{Owner: "acme", RepoAccessible: true}},
	}), nil
}

func useGitHubMigrationTestServer(t *testing.T) *recordingGitHubMigrationService {
	t.Helper()
	service := &recordingGitHubMigrationService{}
	path, handler := civ1connect.NewMigrationServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	return service
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

func representativeOriginAnalysisResponse() *civ2.GetRepositoryMigrationAnalysisResponse {
	return &civ2.GetRepositoryMigrationAnalysisResponse{
		Workflows: []*civ2.RepositoryWorkflowMigration{{
			Path: ".github/workflows/ci.yml",
			Analysis: &civ2.WorkflowAnalysis{
				Blockers: []*civ2.MigrationDiagnostic{{
					Code:       "internal_tracking_DEP-1234",
					Message:    "Cursor Origin does not provide GitHub release APIs.",
					Action:     "softprops/action-gh-release",
					Job:        "release",
					Step:       "Publish release",
					Mode:       "publish-release",
					Capability: "github-releases",
					Severity:   civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_FAILS,
					Workaround: "Publish releases from a GitHub workflow.",
				}},
				Caveats: []*civ2.MigrationDiagnostic{
					{
						Message:    "Dependency submission can appear successful without publishing.",
						Action:     "aquasecurity/trivy-action",
						Job:        "scan",
						Step:       "Publish dependency snapshot",
						Mode:       "dependency-snapshot",
						Capability: "dependency-snapshots",
						Severity:   civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_SILENTLY_DEGRADES,
						Workaround: "Use another output format.",
					},
					{
						Message:    "Runtime input determines whether SARIF upload is attempted.",
						Action:     "github/codeql-action/analyze",
						Job:        "codeql",
						Step:       "Analyze",
						Mode:       "upload-analysis",
						Capability: "code-scanning-sarif",
						Severity:   civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_CONDITIONALLY_UNSUPPORTED,
						Workaround: "Set upload to never.",
					},
				},
			},
		}},
	}
}

func TestMigrateCommandCursorAnalyzesAndRendersSelectedWorkflows(t *testing.T) {
	dir, workflow := newMigrationTestRepository(t)
	runSecretMigrationTestCommand(t, dir, "git", "add", ".")
	runSecretMigrationTestCommand(t, dir, "git", "-c", "user.name=Depot Test", "-c", "user.email=test@depot.dev", "commit", "-m", "base")
	sha := runSecretMigrationTestCommand(t, dir, "git", "rev-parse", "HEAD")
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://github.com/acme/widgets.git")
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "cursor", "git@origin.cursor.com:git/acme/widgets.git")
	runSecretMigrationTestCommand(t, dir, "git", "update-ref", "refs/remotes/origin/main", sha)
	runSecretMigrationTestCommand(t, dir, "git", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	runSecretMigrationTestCommand(t, dir, "git", "update-ref", "refs/remotes/cursor/trunk", sha)
	runSecretMigrationTestCommand(t, dir, "git", "symbolic-ref", "refs/remotes/cursor/HEAD", "refs/remotes/cursor/trunk")

	client := &recordingRepositoryAnalysisClient{response: representativeOriginAnalysisResponse()}
	var output bytes.Buffer
	cmd := newCmdMigrate(migrateOptions{
		dir:                      dir,
		repositoryAnalysisClient: client,
	})
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--yes", "--forge=origin", "--token=depot_api_token", "--org=org-id"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
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
	for _, finding := range []string{
		`  FAILS — .depot/workflows/ci.yml (from .github/workflows/ci.yml)
    Job: release
    Step: Publish release
    Action: softprops/action-gh-release
    Mode: publish-release
    Missing capability: github-releases
    Explanation: Cursor Origin does not provide GitHub release APIs.
    Workaround: Publish releases from a GitHub workflow.`,
		`  SILENT DEGRADATION — .depot/workflows/ci.yml (from .github/workflows/ci.yml)
    Job: scan
    Step: Publish dependency snapshot
    Action: aquasecurity/trivy-action
    Mode: dependency-snapshot
    Missing capability: dependency-snapshots
    Explanation: Dependency submission can appear successful without publishing.
    Workaround: Use another output format.`,
		`  CONDITIONALLY UNSUPPORTED — .depot/workflows/ci.yml (from .github/workflows/ci.yml)
    Job: codeql
    Step: Analyze
    Action: github/codeql-action/analyze
    Mode: upload-analysis
    Missing capability: code-scanning-sarif
    Explanation: Runtime input determines whether SARIF upload is attempted.
    Workaround: Set upload to never.`,
	} {
		if !strings.Contains(output.String(), finding) {
			t.Errorf("command output does not contain finding:\n%s\n\nfull output:\n%s", finding, output.String())
		}
	}
	if strings.Contains(output.String(), "internal_tracking_DEP-1234") {
		t.Fatalf("command output exposed machine-only diagnostic metadata:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "depot ci migrate secrets-and-vars --forge=origin") {
		t.Fatalf("Cursor follow-up command dropped the selected forge:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "pushing and merging them into trunk") {
		t.Fatalf("next steps did not use the selected Cursor remote's default branch:\n%s", output.String())
	}
	if strings.Contains(output.String(), "imported from GitHub") {
		t.Fatalf("Cursor next steps incorrectly describe the source as GitHub:\n%s", output.String())
	}
}

func TestMigrateCommandGitHubModesDoNotRequestOriginAnalysis(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "default", args: []string{"--yes", "--token=depot_api_token", "--org=org-id"}},
		{name: "explicit", args: []string{"--yes", "--forge=github", "--token=depot_api_token", "--org=org-id"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, _ := newMigrationTestRepository(t)
			runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://github.com/acme/widgets.git")
			githubService := useGitHubMigrationTestServer(t)
			client := &recordingRepositoryAnalysisClient{err: errors.New("must not be called")}
			var output bytes.Buffer
			cmd := newCmdMigrate(migrateOptions{
				dir:                      dir,
				repositoryAnalysisClient: client,
			})
			cmd.SetOut(&output)
			cmd.SetArgs(test.args)

			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if client.calls != 0 {
				t.Fatalf("Origin analysis calls = %d, want 0", client.calls)
			}
			if len(githubService.requests) != 1 || githubService.requests[0].Msg.GetRepo() != "acme/widgets" {
				t.Fatalf("unexpected GitHub preflight requests: %#v", githubService.requests)
			}
			if _, err := os.Stat(filepath.Join(dir, ".depot", "workflows", "ci.yml")); err != nil {
				t.Fatalf("GitHub workflow was not migrated: %v", err)
			}
			if !strings.Contains(output.String(), "Migrated 1 workflow(s)") || strings.Contains(output.String(), "Cursor Origin") {
				t.Fatalf("unexpected GitHub migration output:\n%s", output.String())
			}
		})
	}
}

func TestMigrateCommandCompatibleCursorWorkflowStaysQuiet(t *testing.T) {
	dir, _ := newMigrationTestRepository(t)
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://origin.cursor.com/git/acme/widgets")
	client := &recordingRepositoryAnalysisClient{response: &civ2.GetRepositoryMigrationAnalysisResponse{}}
	var output bytes.Buffer
	cmd := newCmdMigrate(migrateOptions{
		dir:                      dir,
		repositoryAnalysisClient: client,
	})
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--yes", "--forge=origin", "--token=depot_org_token"})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("Origin analysis calls = %d, want 1", client.calls)
	}
	if strings.Contains(output.String(), "Cursor Origin compatibility findings:") {
		t.Fatalf("compatible workflow produced compatibility output:\n%s", output.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".depot", "workflows", "ci.yml")); err != nil {
		t.Fatalf("compatible Cursor workflow was not migrated: %v", err)
	}
}

func TestCursorMigrationReportsAnalysisFailureBeforeWritingFiles(t *testing.T) {
	dir, _ := newMigrationTestRepository(t)
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://origin.cursor.com/git/acme/widgets")
	client := &recordingRepositoryAnalysisClient{err: connect.NewError(connect.CodePermissionDenied, errors.New("repository is not available to this organization"))}

	err := workflowsWithContext(context.Background(), migrateOptions{
		dir:                      dir,
		yes:                      true,
		forge:                    migrationForgeOrigin,
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
		forge:                    migrationForgeOrigin,
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
		forge:                    migrationForgeOrigin,
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
		forge:                    migrationForgeOrigin,
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

func newMigrationTestRepository(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runSecretMigrationTestCommand(t, "", "git", "init", dir)
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatal(err)
	}
	workflow := "name: CI\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n      - run: echo ${{ secrets.MY_SECRET }}\n"
	if err := os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte(workflow), 0644); err != nil {
		t.Fatal(err)
	}
	return dir, workflow
}
