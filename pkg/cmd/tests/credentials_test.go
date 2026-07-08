package tests

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/depot/cli/pkg/oidc"
)

type fakeOIDCProvider struct {
	name          string
	token         string
	err           error
	retrieveToken func() (string, error)
}

func (p fakeOIDCProvider) Name() string {
	return p.name
}

func (p fakeOIDCProvider) RetrieveToken(context.Context) (string, error) {
	if p.retrieveToken != nil {
		return p.retrieveToken()
	}
	return p.token, p.err
}

func TestResolveOIDCCredentialReturnsFirstToken(t *testing.T) {
	token, err := resolveOIDCCredentialWithProviders(context.Background(), []oidc.OIDCProvider{
		fakeOIDCProvider{name: "empty"},
		fakeOIDCProvider{name: "github", token: " token-1 "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-1" {
		t.Fatalf("expected trimmed token, got %q", token)
	}
}

func TestResolveOIDCCredentialIgnoresProviderErrors(t *testing.T) {
	_, err := resolveOIDCCredentialWithProviders(context.Background(), []oidc.OIDCProvider{
		fakeOIDCProvider{name: "github", err: errors.New("missing permission")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing OIDC credential") {
		t.Fatalf("expected missing credential error, got %q", err.Error())
	}
	if strings.Contains(strings.ToLower(err.Error()), "github") {
		t.Fatalf("expected generic missing credential error, got %q", err.Error())
	}
}

func TestResolveOIDCCredentialPreservesContextErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveOIDCCredentialWithProviders(context.Background(), []oidc.OIDCProvider{
				fakeOIDCProvider{name: "github", err: tc.err},
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("expected %v, got %v", tc.err, err)
			}
		})
	}
}

func TestResolveOIDCCredentialReportsMissingCredential(t *testing.T) {
	_, err := resolveOIDCCredentialWithProviders(context.Background(), []oidc.OIDCProvider{
		fakeOIDCProvider{name: "empty"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing OIDC credential") {
		t.Fatalf("expected missing credential error, got %q", err.Error())
	}
}

func TestConfigureDepotCIOIDCEnv(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_NAME", depotCIRunnerName)
	t.Setenv("DEPOT_ORG_ID", "org-1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	configureDepotCIOIDCEnv()

	if got := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"); got != depotCIOIDCRequestURL {
		t.Fatalf("expected request URL %q, got %q", depotCIOIDCRequestURL, got)
	}
	if got := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"); got != depotCIOIDCRequestToken {
		t.Fatalf("expected request token %q, got %q", depotCIOIDCRequestToken, got)
	}
}

func TestConfigureDepotCIOIDCEnvPreservesExistingValues(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_NAME", depotCIRunnerName)
	t.Setenv("DEPOT_ORG_ID", "org-1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://existing")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "existing-token")

	configureDepotCIOIDCEnv()

	if got := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"); got != "https://existing" {
		t.Fatalf("expected existing request URL, got %q", got)
	}
	if got := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"); got != "existing-token" {
		t.Fatalf("expected existing request token, got %q", got)
	}
}

func TestConfigureDepotCIOIDCEnvDoesNotSetValuesOutsideDepotCI(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_NAME", "GitHub Actions")
	t.Setenv("DEPOT_ORG_ID", "org-1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	configureDepotCIOIDCEnv()

	if got := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"); got != "" {
		t.Fatalf("expected empty request URL outside Depot CI, got %q", got)
	}
	if got := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"); got != "" {
		t.Fatalf("expected empty request token outside Depot CI, got %q", got)
	}
}

func TestResolveOIDCCredentialRestoresInjectedDepotCIEnv(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_NAME", depotCIRunnerName)
	t.Setenv("DEPOT_ORG_ID", "org-1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	token, err := resolveOIDCCredentialWithDepotCIEnv(context.Background(), []oidc.OIDCProvider{
		fakeOIDCProvider{
			name: "github",
			retrieveToken: func() (string, error) {
				if got := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"); got != depotCIOIDCRequestURL {
					t.Fatalf("expected request URL during retrieval %q, got %q", depotCIOIDCRequestURL, got)
				}
				if got := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"); got != depotCIOIDCRequestToken {
					t.Fatalf("expected request token during retrieval %q, got %q", depotCIOIDCRequestToken, got)
				}
				return "token-1", nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-1" {
		t.Fatalf("expected token-1, got %q", token)
	}
	if got := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"); got != "" {
		t.Fatalf("expected request URL to be restored after retrieval, got %q", got)
	}
	if got := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"); got != "" {
		t.Fatalf("expected request token to be restored after retrieval, got %q", got)
	}
}

func TestIsRunningInDepotCI(t *testing.T) {
	env := map[string]string{
		"GITHUB_ACTIONS": "true",
		"RUNNER_NAME":    depotCIRunnerName,
		"DEPOT_ORG_ID":   "org-1",
	}

	if !isRunningInDepotCI(func(key string) string { return env[key] }) {
		t.Fatal("expected Depot CI environment")
	}
	env["DEPOT_ORG_ID"] = " "
	if isRunningInDepotCI(func(key string) string { return env[key] }) {
		t.Fatal("expected missing org to disable Depot CI environment")
	}
}
