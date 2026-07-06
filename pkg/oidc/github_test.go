package oidc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
