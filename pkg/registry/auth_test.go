package registry

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/depot/cli/pkg/build"
	"github.com/moby/buildkit/session/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type stubAuthServer struct {
	getTokenAuthorityCalled bool
}

func (s *stubAuthServer) Credentials(context.Context, *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	return &auth.CredentialsResponse{Username: "inner-user", Secret: "inner-secret"}, nil
}

func (s *stubAuthServer) FetchToken(context.Context, *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	return &auth.FetchTokenResponse{Token: "inner-token"}, nil
}

func (s *stubAuthServer) GetTokenAuthority(context.Context, *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	s.getTokenAuthorityCalled = true
	return &auth.GetTokenAuthorityResponse{PublicKey: []byte("inner-key")}, nil
}

func (s *stubAuthServer) VerifyTokenAuthority(context.Context, *auth.VerifyTokenAuthorityRequest) (*auth.VerifyTokenAuthorityResponse, error) {
	return &auth.VerifyTokenAuthorityResponse{Signed: []byte("inner-signed")}, nil
}

func TestAuthProviderCredentialsUsesExplicitCredential(t *testing.T) {
	token := base64.StdEncoding.EncodeToString([]byte("x-token:secret"))
	p := AuthProvider{
		inner: &stubAuthServer{},
		credentials: []build.Credential{
			{Host: "registry.depot.dev", Token: token},
		},
	}

	resp, err := p.Credentials(context.Background(), &auth.CredentialsRequest{Host: "registry.depot.dev:443"})
	if err != nil {
		t.Fatalf("Credentials returned error: %v", err)
	}
	if resp.Username != "x-token" || resp.Secret != "secret" {
		t.Fatalf("unexpected credentials: %+v", resp)
	}
}

func TestAuthProviderGetTokenAuthorityDisabledForExplicitCredential(t *testing.T) {
	token := base64.StdEncoding.EncodeToString([]byte("x-token:secret"))
	inner := &stubAuthServer{}
	p := AuthProvider{
		inner: inner,
		credentials: []build.Credential{
			{Host: "registry.depot.dev", Token: token},
		},
	}

	_, err := p.GetTokenAuthority(context.Background(), &auth.GetTokenAuthorityRequest{Host: "registry.depot.dev"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected unavailable error, got: %v", err)
	}
	if inner.getTokenAuthorityCalled {
		t.Fatalf("inner GetTokenAuthority should not be called for explicit credential host")
	}
}

func TestAuthProviderGetTokenAuthorityDelegatesForOtherHosts(t *testing.T) {
	token := base64.StdEncoding.EncodeToString([]byte("x-token:secret"))
	inner := &stubAuthServer{}
	p := AuthProvider{
		inner: inner,
		credentials: []build.Credential{
			{Host: "registry.depot.dev", Token: token},
		},
	}

	resp, err := p.GetTokenAuthority(context.Background(), &auth.GetTokenAuthorityRequest{Host: "ghcr.io"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !inner.getTokenAuthorityCalled {
		t.Fatalf("expected inner GetTokenAuthority to be called")
	}
	if string(resp.PublicKey) != "inner-key" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}
