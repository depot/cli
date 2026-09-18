package api

import (
	"context"

	"connectrpc.com/connect"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
	"github.com/depot/cli/pkg/proto/depot/ci/v2/civ2connect"
)

// WorkflowMigrationClient is the portion of the workflow migration API used by Depot CLI flows.
// Keeping this interface narrow lets commands substitute deterministic clients in tests.
type WorkflowMigrationClient interface {
	GetRepositoryAnalysis(context.Context, *connect.Request[civ2.GetRepositoryMigrationAnalysisRequest]) (*connect.Response[civ2.GetRepositoryMigrationAnalysisResponse], error)
	RegisterSecretMigrationIntent(context.Context, *connect.Request[civ2.SecretMigrationIntent]) (*connect.Response[civ2.RegisterSecretMigrationIntentResponse], error)
}

func NewWorkflowMigrationClient() WorkflowMigrationClient {
	baseURL := baseURLFunc()
	return civ2connect.NewMigrationServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}

var _ WorkflowMigrationClient = (civ2connect.MigrationServiceClient)(nil)
