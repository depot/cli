package ci

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

type recordingBatchSecretService struct {
	civ2connect.UnimplementedSecretServiceHandler
	t     *testing.T
	calls int
}

func (s *recordingBatchSecretService) BatchAddRepoSecrets(
	_ context.Context,
	req *connect.Request[civ2.BatchAddRepoSecretsRequest],
) (*connect.Response[civ2.BatchAddRepoSecretsResponse], error) {
	s.calls++
	if req.Msg.GetRepo() != "namespace/repo" {
		s.t.Fatalf("repo = %q, want namespace/repo", req.Msg.GetRepo())
	}
	assertSecretInputs(s.t, req.Msg.GetSecrets(), map[string]string{"A_SECRET": "alpha", "B_SECRET": "beta"})
	return connect.NewResponse(&civ2.BatchAddRepoSecretsResponse{}), nil
}

type recordingBatchVariableService struct {
	civ2connect.UnimplementedVariableServiceHandler
	t     *testing.T
	calls int
}

func (s *recordingBatchVariableService) BatchAddRepoVariables(
	_ context.Context,
	req *connect.Request[civ2.BatchAddRepoVariablesRequest],
) (*connect.Response[civ2.BatchAddRepoVariablesResponse], error) {
	s.calls++
	if req.Msg.GetRepo() != "namespace/repo" {
		s.t.Fatalf("repo = %q, want namespace/repo", req.Msg.GetRepo())
	}
	got := make(map[string]string, len(req.Msg.GetVariables()))
	for _, variable := range req.Msg.GetVariables() {
		got[variable.GetName()] = variable.GetValue()
	}
	want := map[string]string{"A_VAR": "alpha", "B_VAR": "beta"}
	if len(got) != len(want) {
		s.t.Fatalf("variables = %#v, want %#v", got, want)
	}
	for name, value := range want {
		if got[name] != value {
			s.t.Fatalf("variable %s = %q, want %q", name, got[name], value)
		}
	}
	return connect.NewResponse(&civ2.BatchAddRepoVariablesResponse{}), nil
}

func TestAddCommandsUseOneBatchRequestForMigrationShape(t *testing.T) {
	secretService := &recordingBatchSecretService{t: t}
	variableService := &recordingBatchVariableService{t: t}
	secretPath, secretHandler := civ2connect.NewSecretServiceHandler(secretService)
	variablePath, variableHandler := civ2connect.NewVariableServiceHandler(variableService)
	mux := http.NewServeMux()
	mux.Handle(secretPath, secretHandler)
	mux.Handle(variablePath, variableHandler)
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	t.Setenv("GITHUB_REF_NAME", "depot-migrate-secrets-0123456789")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://example.invalid/oidc")

	secretCmd := NewCmdSecretsAdd()
	secretCmd.SetArgs([]string{
		"A_SECRET=alpha",
		"EMPTY_SECRET=",
		"GITHUB_TOKEN=ephemeral-token",
		"B_SECRET=beta",
		"--repo", "namespace/repo",
		"--token", "depot_api_token",
		"--org", "org-id",
	})
	if err := secretCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	emptySecretCmd := NewCmdSecretsAdd()
	emptySecretCmd.SetArgs([]string{
		"EMPTY_SECRET=",
		"GITHUB_TOKEN=ephemeral-token",
		"--repo", "namespace/repo",
		"--token", "depot_api_token",
		"--org", "org-id",
	})
	if err := emptySecretCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	variableCmd := NewCmdVarsAdd()
	variableCmd.SetArgs([]string{
		"A_VAR=alpha",
		"B_VAR=beta",
		"--repo", "namespace/repo",
		"--token", "depot_api_token",
		"--org", "org-id",
	})
	if err := variableCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	if secretService.calls != 1 {
		t.Fatalf("secret batch calls = %d, want 1", secretService.calls)
	}
	if variableService.calls != 1 {
		t.Fatalf("variable batch calls = %d, want 1", variableService.calls)
	}
}

func assertSecretInputs(t *testing.T, inputs []*civ2.SecretInput, want map[string]string) {
	t.Helper()
	got := make(map[string]string, len(inputs))
	for _, secret := range inputs {
		got[secret.GetName()] = secret.GetValue()
	}
	if len(got) != len(want) {
		t.Fatalf("secrets = %#v, want %#v", got, want)
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("secret %s = %q, want %q", name, got[name], value)
		}
	}
}
