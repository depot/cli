package registry

import (
	"context"
	"testing"

	"github.com/moby/buildkit/session/auth"
)

func TestShouldUseDepotCredentialsForCurrentProjectScope(t *testing.T) {
	provider := &AuthProvider{projectID: "current-project"}

	if !provider.shouldUseDepotCredentials([]string{"repository:current-project:pull,push"}) {
		t.Fatal("expected current project scope to use Depot credentials")
	}
}

func TestShouldUseDepotCredentialsFallsBackForOtherProjectScope(t *testing.T) {
	provider := &AuthProvider{projectID: "current-project"}

	if provider.shouldUseDepotCredentials([]string{"repository:other-project:pull"}) {
		t.Fatal("expected other project scope to use Docker credentials")
	}
}

func TestFetchTokenFallsBackForOtherProjectScope(t *testing.T) {
	inner := &recordingAuthServer{}
	provider := &AuthProvider{
		inner:       inner,
		projectID:   "current-project",
		credentials: nil,
	}

	_, err := provider.FetchToken(context.Background(), &auth.FetchTokenRequest{
		Host:   "registry.depot.dev",
		Scopes: []string{"repository:other-project:pull"},
	})
	if err != nil {
		t.Fatalf("FetchToken returned error: %v", err)
	}
	if !inner.fetchTokenCalled {
		t.Fatal("expected FetchToken to fall back to inner auth server")
	}
}

type recordingAuthServer struct {
	auth.UnimplementedAuthServer
	fetchTokenCalled bool
}

func (r *recordingAuthServer) FetchToken(context.Context, *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	r.fetchTokenCalled = true
	return &auth.FetchTokenResponse{Token: "docker-token"}, nil
}
