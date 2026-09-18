package oidc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubOIDCProviderHonorsCanceledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"value":"token-1"}`))
	}))
	t.Cleanup(server.Close)

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL+"?token=1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	token, err := NewGitHubOIDCProvider().RetrieveToken(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got token=%q err=%v", token, err)
	}
	if token != "" {
		t.Fatalf("expected no token from canceled context, got %q", token)
	}
}

func TestGitHubOIDCProviderIgnoresSecretMigrationIntentForDefaultAudience(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Query().Get("audience"), "https://depot.dev"; got != want {
			t.Fatalf("audience = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"value":"token-1"}`))
	}))
	t.Cleanup(server.Close)

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL+"?token=1")
	t.Setenv("GITHUB_REF_NAME", "depot-migrate-secrets-0123456789")

	token, err := NewGitHubOIDCProvider().RetrieveToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-1" {
		t.Fatalf("token = %q, want token-1", token)
	}
}

func TestSecretMigrationIntentIDFromGitHubRef(t *testing.T) {
	if got, want := SecretMigrationIntentIDFromGitHubRef("depot-migrate-secrets-0123456789"), "0123456789"; got != want {
		t.Fatalf("intent ID = %q, want %q", got, want)
	}
	for _, refName := range []string{"", "main", "0123456789", "depot-migrate-secrets-abc", "depot-migrate-secrets-012345678a"} {
		if got := SecretMigrationIntentIDFromGitHubRef(refName); got != "" {
			t.Fatalf("intent ID for malformed ref %q = %q, want empty", refName, got)
		}
	}
}

func TestSecretMigrationIntentIDFromGitHubRefWithPrefix(t *testing.T) {
	if got, want := SecretMigrationIntentIDFromGitHubRefWithPrefix("automation/depot-0123456789", "automation/depot-"), "0123456789"; got != want {
		t.Fatalf("intent ID = %q, want %q", got, want)
	}
	for _, refName := range []string{"automation/depot-abc", "other/depot-0123456789", "0123456789"} {
		if got := SecretMigrationIntentIDFromGitHubRefWithPrefix(refName, "automation/depot-"); got != "" {
			t.Fatalf("intent ID for malformed ref %q = %q, want empty", refName, got)
		}
	}
}

func TestSecretMigrationIntentIDFromGitHubActionsEnvironment(t *testing.T) {
	t.Setenv("GITHUB_REF_NAME", "depot-migrate-secrets-0123456789")
	if got := SecretMigrationIntentIDFromGitHubActionsEnvironment(); got != "" {
		t.Fatalf("intent ID without GitHub OIDC environment = %q, want empty", got)
	}

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://example.invalid/oidc")
	if got, want := SecretMigrationIntentIDFromGitHubActionsEnvironment(), "0123456789"; got != want {
		t.Fatalf("intent ID = %q, want %q", got, want)
	}

	t.Setenv("GITHUB_REF_NAME", "automation/depot-0123456789")
	t.Setenv(SecretMigrationBranchPrefixEnv, "automation/depot-")
	if got, want := SecretMigrationIntentIDFromGitHubActionsEnvironment(), "0123456789"; got != want {
		t.Fatalf("custom-prefix intent ID = %q, want %q", got, want)
	}
}

func TestGitHubOIDCProviderRequestsSecretMigrationAudienceExplicitly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Query().Get("audience"), "https://depot.dev/ci/secret-migration/0123456789"; got != want {
			t.Fatalf("audience = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"value":"token-1"}`))
	}))
	t.Cleanup(server.Close)

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL+"?token=1")

	token, err := NewGitHubOIDCProvider().RetrieveSecretMigrationToken(context.Background(), "0123456789")
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-1" {
		t.Fatalf("token = %q, want token-1", token)
	}
}

func TestGitHubOIDCProviderRejectsMalformedSecretMigrationIntentID(t *testing.T) {
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://example.invalid/token")

	if _, err := NewGitHubOIDCProvider().RetrieveSecretMigrationToken(context.Background(), "abc"); err == nil {
		t.Fatal("expected malformed intent ID to be rejected")
	}
}

func TestGitHubOIDCProviderRejectsNonOKResponses(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "GitHub JSON error", contentType: "application/json", body: `{"message":"rate limited"}`},
		{name: "proxy HTML error", contentType: "text/html", body: `<html>unavailable</html>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)

			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL)

			token, err := NewGitHubOIDCProvider().RetrieveToken(context.Background())
			if err == nil {
				t.Fatalf("expected HTTP failure, got token %q", token)
			}
			if token != "" {
				t.Fatalf("token = %q, want empty", token)
			}
			if !strings.Contains(err.Error(), "429 Too Many Requests") || !strings.Contains(err.Error(), test.body) {
				t.Fatalf("error = %q, want status and bounded response body", err)
			}
		})
	}
}

func TestGitHubOIDCProviderRejectsEmptyToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"value":""}`))
	}))
	t.Cleanup(server.Close)

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL)

	token, err := NewGitHubOIDCProvider().RetrieveToken(context.Background())
	if err == nil {
		t.Fatalf("expected empty token to be rejected, got token %q", token)
	}
	if token != "" {
		t.Fatalf("token = %q, want empty", token)
	}
	if !strings.Contains(err.Error(), "did not include a token") {
		t.Fatalf("error = %q, want missing token error", err)
	}
}
