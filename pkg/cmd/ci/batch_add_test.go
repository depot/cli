package ci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
	"github.com/depot/cli/pkg/proto/depot/ci/v2/civ2connect"
	civ3beta2 "github.com/depot/cli/pkg/proto/depot/ci/v3beta2"
	"github.com/depot/cli/pkg/proto/depot/ci/v3beta2/civ3beta2connect"
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

type recordingVariantSecretService struct {
	civ3beta2connect.UnimplementedSecretServiceHandler
	requests []*civ3beta2.SetSecretVariantRequest
}

func (s *recordingVariantSecretService) SetSecretVariant(
	_ context.Context,
	req *connect.Request[civ3beta2.SetSecretVariantRequest],
) (*connect.Response[civ3beta2.SetSecretVariantResponse], error) {
	s.requests = append(s.requests, req.Msg)
	return connect.NewResponse(&civ3beta2.SetSecretVariantResponse{}), nil
}

type recordingVariantVariableService struct {
	civ3beta2connect.UnimplementedVariableServiceHandler
	requests []*civ3beta2.SetVariableVariantRequest
}

func (s *recordingVariantVariableService) SetVariableVariant(
	_ context.Context,
	req *connect.Request[civ3beta2.SetVariableVariantRequest],
) (*connect.Response[civ3beta2.SetVariableVariantResponse], error) {
	s.requests = append(s.requests, req.Msg)
	return connect.NewResponse(&civ3beta2.SetVariableVariantResponse{}), nil
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
		"github_token=ephemeral-token",
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
		"EMPTY_VAR=",
		"B_VAR=beta",
		"--repo", "namespace/repo",
		"--token", "depot_api_token",
		"--org", "org-id",
	})
	if err := variableCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	emptyVariableCmd := NewCmdVarsAdd()
	emptyVariableCmd.SetArgs([]string{
		"EMPTY_VAR=",
		"--repo", "namespace/repo",
		"--token", "depot_api_token",
		"--org", "org-id",
	})
	if err := emptyVariableCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	if secretService.calls != 1 {
		t.Fatalf("secret batch calls = %d, want 1", secretService.calls)
	}
	if variableService.calls != 1 {
		t.Fatalf("variable batch calls = %d, want 1", variableService.calls)
	}
}

func TestBulkAddCommandsUseVariantRPCsOutsideMigration(t *testing.T) {
	secretService := &recordingVariantSecretService{}
	variableService := &recordingVariantVariableService{}
	secretPath, secretHandler := civ3beta2connect.NewSecretServiceHandler(secretService)
	variablePath, variableHandler := civ3beta2connect.NewVariableServiceHandler(variableService)
	mux := http.NewServeMux()
	mux.Handle(secretPath, secretHandler)
	mux.Handle(variablePath, variableHandler)
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)

	secretCmd := NewCmdSecretsAdd()
	secretCmd.SetArgs([]string{
		"A_SECRET=alpha",
		"B_SECRET=beta",
		"--repo", "namespace/repo",
		"--token", "depot_api_token",
		"--org", "org-id",
	})
	if err := secretCmd.ExecuteContext(context.Background()); err != nil {
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

	assertSecretVariantRequests(t, secretService.requests, []string{"A_SECRET", "B_SECRET"})
	assertVariableVariantRequests(t, variableService.requests, []string{"A_VAR", "B_VAR"})
}

func assertSecretVariantRequests(t *testing.T, requests []*civ3beta2.SetSecretVariantRequest, names []string) {
	t.Helper()
	if len(requests) != len(names) {
		t.Fatalf("secret variant requests = %d, want %d", len(requests), len(names))
	}
	for i, request := range requests {
		if request.GetSecretName() != names[i] || request.GetVariantName() != "default" {
			t.Fatalf("secret request %d = (%q, %q), want (%q, default)", i, request.GetSecretName(), request.GetVariantName(), names[i])
		}
		assertRepoAttribute(t, request.GetAttributes())
	}
}

func assertVariableVariantRequests(t *testing.T, requests []*civ3beta2.SetVariableVariantRequest, names []string) {
	t.Helper()
	if len(requests) != len(names) {
		t.Fatalf("variable variant requests = %d, want %d", len(requests), len(names))
	}
	for i, request := range requests {
		if request.GetVariableName() != names[i] || request.GetVariantName() != "default" {
			t.Fatalf("variable request %d = (%q, %q), want (%q, default)", i, request.GetVariableName(), request.GetVariantName(), names[i])
		}
		assertRepoAttribute(t, request.GetAttributes())
	}
}

func assertRepoAttribute(t *testing.T, attributes []*civ3beta2.Attribute) {
	t.Helper()
	if len(attributes) != 1 || attributes[0].GetKey() != "repository" || attributes[0].GetValue() != "namespace/repo" {
		t.Fatalf("attributes = %#v, want repository=namespace/repo", attributes)
	}
}

func TestMigrationAddCommandsWarnAboutSkippedEmptyValues(t *testing.T) {
	t.Setenv("GITHUB_REF_NAME", "depot-migrate-secrets-0123456789")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://example.invalid/oidc")

	output, err := captureStdout(t, func() error {
		secretCmd := NewCmdSecretsAdd()
		secretCmd.SetArgs([]string{
			"FIRST_SECRET=",
			"SECOND_SECRET=",
			"--repo", "namespace/repo",
			"--token", "depot_api_token",
			"--org", "org-id",
		})
		if err := secretCmd.ExecuteContext(context.Background()); err != nil {
			return err
		}

		variableCmd := NewCmdVarsAdd()
		variableCmd.SetArgs([]string{
			"ONLY_VAR=",
			"--repo", "namespace/repo",
			"--token", "depot_api_token",
			"--org", "org-id",
		})
		return variableCmd.ExecuteContext(context.Background())
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{
		"Warning: skipped 2 secrets during migration because their values were empty",
		"Warning: skipped 1 variable during migration because their values were empty",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("output does not contain %q:\n%s", expected, output)
		}
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
