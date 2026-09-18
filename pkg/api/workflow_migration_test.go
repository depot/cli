package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
	"github.com/depot/cli/pkg/proto/depot/ci/v2/civ2connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type workflowMigrationTestHandler struct {
	civ2connect.UnimplementedMigrationServiceHandler

	analysisRequest *connect.Request[civ2.GetRepositoryMigrationAnalysisRequest]
	intentRequest   *connect.Request[civ2.SecretMigrationIntent]
}

func (h *workflowMigrationTestHandler) GetRepositoryAnalysis(
	_ context.Context,
	request *connect.Request[civ2.GetRepositoryMigrationAnalysisRequest],
) (*connect.Response[civ2.GetRepositoryMigrationAnalysisResponse], error) {
	h.analysisRequest = request
	return connect.NewResponse(&civ2.GetRepositoryMigrationAnalysisResponse{
		Repository: &civ2.MigrationRepository{Id: "repository-1", Name: "api", FullName: "acme/api"},
		Workflows: []*civ2.RepositoryWorkflowMigration{
			{
				Path: ".github/workflows/release.yml",
				Analysis: &civ2.WorkflowAnalysis{
					MigrationReadiness: civ2.MigrationReadiness_MIGRATION_READINESS_READY_WITH_CAVEATS,
					Caveats: []*civ2.MigrationDiagnostic{
						{
							Code:       "partial_github_releases",
							Severity:   civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_FAILS,
							Capability: "github-releases",
						},
					},
				},
			},
		},
	}), nil
}

func (h *workflowMigrationTestHandler) RegisterSecretMigrationIntent(
	_ context.Context,
	request *connect.Request[civ2.SecretMigrationIntent],
) (*connect.Response[civ2.RegisterSecretMigrationIntentResponse], error) {
	h.intentRequest = request
	return connect.NewResponse(&civ2.RegisterSecretMigrationIntentResponse{Id: "0123456789"}), nil
}

func TestWorkflowMigrationClientSupportsAnalysisAndSecretIntents(t *testing.T) {
	handler := &workflowMigrationTestHandler{}
	path, connectHandler := civ2connect.NewMigrationServiceHandler(handler)
	mux := http.NewServeMux()
	mux.Handle(path, connectHandler)
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)

	originalBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = originalBaseURLFunc })

	client := NewWorkflowMigrationClient()
	analysisResponse, err := client.GetRepositoryAnalysis(
		context.Background(),
		WithAuthenticationAndOrg(connect.NewRequest(&civ2.GetRepositoryMigrationAnalysisRequest{
			Forge:         civ2.MigrationForge_MIGRATION_FORGE_CURSOR_ORIGIN,
			RepositoryUrl: "origin.cursor.com/acme/api",
			WorkflowFiles: &civ2.MigrationWorkflowFiles{Workflows: []*civ2.MigrationWorkflowFile{
				{Path: ".github/workflows/release.yml", Content: "on: push"},
			}},
		}), "depot-token", "org-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := handler.analysisRequest.Header().Get("Authorization"); got != "Bearer depot-token" {
		t.Fatalf("analysis authorization header = %q", got)
	}
	if got := handler.analysisRequest.Header().Get("x-depot-org"); got != "org-1" {
		t.Fatalf("analysis organization header = %q", got)
	}
	if got := handler.analysisRequest.Msg.GetForge(); got != civ2.MigrationForge_MIGRATION_FORGE_CURSOR_ORIGIN {
		t.Fatalf("analysis forge = %v", got)
	}
	if got := handler.analysisRequest.Msg.GetRepositoryUrl(); got != "origin.cursor.com/acme/api" {
		t.Fatalf("analysis repository URL = %q", got)
	}
	if got := handler.analysisRequest.Msg.GetWorkflowFiles().GetWorkflows()[0].GetContent(); got != "on: push" {
		t.Fatalf("analysis workflow content = %q", got)
	}
	if got := analysisResponse.Msg.GetWorkflows()[0].GetAnalysis().GetCaveats()[0].GetSeverity(); got != civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_FAILS {
		t.Fatalf("analysis diagnostic severity = %v", got)
	}

	intentResponse, err := client.RegisterSecretMigrationIntent(
		context.Background(),
		WithAuthenticationAndOrg(connect.NewRequest(&civ2.SecretMigrationIntent{
			Sha:           "0123456789012345678901234567890123456789",
			RepositoryUrl: "github.com/depot/cli",
		}), "depot-token", "org-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := handler.intentRequest.Msg.GetRepositoryUrl(); got != "github.com/depot/cli" {
		t.Fatalf("intent repository URL = %q", got)
	}
	if got := intentResponse.Msg.GetId(); got != "0123456789" {
		t.Fatalf("intent ID = %q", got)
	}
}
