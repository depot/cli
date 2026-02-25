package registry

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	authutil "github.com/containerd/containerd/remotes/docker/auth"
	remoteserrors "github.com/containerd/containerd/remotes/errors"
	"github.com/depot/cli/pkg/build"
	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

var (
	_ auth.AuthServer    = (*AuthProvider)(nil)
	_ session.Attachable = (*AuthProvider)(nil)
)

type AuthProvider struct {
	inner       auth.AuthServer
	credentials []build.Credential
}

// NewAuthProvider searches the session.Attachables for the first auth.AuthServer,
// wraps it in an AuthProvider, and returns it.
func ReplaceDockerAuth(credentials []build.Credential, as []session.Attachable) []session.Attachable {
	dockerConfig := config.LoadDefaultConfigFile(os.Stderr)
	for _, c := range credentials {
		dockerConfig.AuthConfigs[c.Host] = types.AuthConfig{
			Auth:          c.Token,
			ServerAddress: c.Host,
		}
	}

	for i, a := range as {
		if _, ok := a.(auth.AuthServer); ok {
			p := authprovider.NewDockerAuthProvider(dockerConfig)
			as[i] = &AuthProvider{
				credentials: credentials,
				inner:       p.(auth.AuthServer),
			}
		}
	}

	return as
}

func (a *AuthProvider) Register(server *grpc.Server) {
	auth.RegisterAuthServer(server, a)
}

func (a *AuthProvider) Credentials(ctx context.Context, req *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	for _, c := range a.credentials {
		if c.Host == req.Host {
			decodedAuth, err := base64.StdEncoding.DecodeString(c.Token)
			if err != nil {
				return nil, err
			}

			usernamePassword := strings.SplitN(string(decodedAuth), ":", 2)
			if len(usernamePassword) != 2 {
				return nil, fmt.Errorf("invalid auth string")
			}

			return &auth.CredentialsResponse{
				Username: usernamePassword[0],
				Secret:   usernamePassword[1],
			}, nil
		}
	}

	return a.inner.Credentials(ctx, req)
}

func (a *AuthProvider) FetchToken(ctx context.Context, req *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	creds, err := a.findCredentials(req.Host)
	if err != nil {
		return nil, err
	}
	if creds == nil {
		return a.inner.FetchToken(ctx, req)
	}

	if creds.Password != "" {
		return fetchTokenWithFallback(ctx, req, creds)
	}

	// No secret, fall back to inner provider.
	return a.inner.FetchToken(ctx, req)
}

// findCredentials looks up decoded credentials for a host from the depot credential list.
func (a *AuthProvider) findCredentials(host string) (*types.AuthConfig, error) {
	for _, c := range a.credentials {
		if c.Host != host {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(c.Token)
		if err != nil {
			return nil, err
		}
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid auth string")
		}
		return &types.AuthConfig{
			Username: parts[0],
			Password: parts[1],
		}, nil
	}
	return nil, nil
}

// fetchTokenWithFallback attempts OAuth POST first, falling back to GET for registries
// that don't support POST (e.g., GCR returns 404, JFrog returns 401, ACR returns 400).
func fetchTokenWithFallback(ctx context.Context, req *auth.FetchTokenRequest, creds *types.AuthConfig) (*auth.FetchTokenResponse, error) {
	to := authutil.TokenOptions{
		Realm:    req.Realm,
		Service:  req.Service,
		Scopes:   req.Scopes,
		Username: creds.Username,
		Secret:   creds.Password,
	}

	resp, err := authutil.FetchTokenWithOAuth(ctx, http.DefaultClient, nil, "buildkit-client", to)
	if err != nil {
		var errStatus remoteserrors.ErrUnexpectedStatus
		if errors.As(err, &errStatus) {
			// Registries without support for POST may return various error codes:
			// - GCR: 404
			// - JFrog Artifactory: 401
			// - ACR: 400
			// Fall back to GET for any unexpected status.
			getResp, err := authutil.FetchToken(ctx, http.DefaultClient, nil, to)
			if err != nil {
				return nil, err
			}
			return toFetchTokenResponse(getResp.Token, getResp.IssuedAt, getResp.ExpiresIn), nil
		}
		return nil, err
	}
	return toFetchTokenResponse(resp.AccessToken, resp.IssuedAt, resp.ExpiresIn), nil
}

func toFetchTokenResponse(token string, issuedAt time.Time, expires int) *auth.FetchTokenResponse {
	resp := &auth.FetchTokenResponse{
		Token:     token,
		ExpiresIn: int64(expires),
	}
	if !issuedAt.IsZero() {
		resp.IssuedAt = issuedAt.Unix()
	}
	return resp
}

func (a *AuthProvider) GetTokenAuthority(ctx context.Context, req *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	return a.inner.GetTokenAuthority(ctx, req)
}

func (a *AuthProvider) VerifyTokenAuthority(ctx context.Context, req *auth.VerifyTokenAuthorityRequest) (*auth.VerifyTokenAuthorityResponse, error) {
	return a.inner.VerifyTokenAuthority(ctx, req)
}

// DepotAuthProvider wraps the Docker auth provider to add support for
// DEPOT_PUSH_REGISTRY_AUTH environment variables.
type DepotAuthProvider struct {
	inner auth.AuthServer
}

// NewDockerAuthProviderWithDepotAuth creates a new Docker auth provider that supports
// DEPOT_PUSH_REGISTRY_AUTH environment variables in addition to regular Docker config.
func NewDockerAuthProviderWithDepotAuth() session.Attachable {
	dockerConfig := config.LoadDefaultConfigFile(os.Stderr)
	innerProvider := authprovider.NewDockerAuthProvider(dockerConfig)

	return &DepotAuthProvider{
		inner: innerProvider.(auth.AuthServer),
	}
}

func (a *DepotAuthProvider) Register(server *grpc.Server) {
	auth.RegisterAuthServer(server, a)
}

func (a *DepotAuthProvider) Credentials(ctx context.Context, req *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	// First try to get credentials from DEPOT environment variables
	if creds := GetDepotAuthConfig(); creds != nil {
		return &auth.CredentialsResponse{
			Username: creds.Username,
			Secret:   creds.Password,
		}, nil
	}

	// Fall back to the default Docker auth provider
	return a.inner.Credentials(ctx, req)
}

func (a *DepotAuthProvider) FetchToken(ctx context.Context, req *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	creds := GetDepotAuthConfig()
	if creds == nil {
		return a.inner.FetchToken(ctx, req)
	}

	if creds.Password != "" {
		return fetchTokenWithFallback(ctx, req, creds)
	}

	return a.inner.FetchToken(ctx, req)
}

func (a *DepotAuthProvider) GetTokenAuthority(ctx context.Context, req *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	return a.inner.GetTokenAuthority(ctx, req)
}

func (a *DepotAuthProvider) VerifyTokenAuthority(ctx context.Context, req *auth.VerifyTokenAuthorityRequest) (*auth.VerifyTokenAuthorityResponse, error) {
	return a.inner.VerifyTokenAuthority(ctx, req)
}

// GetDepotAuthConfig retrieves credentials from DEPOT_PUSH_REGISTRY_AUTH environment variables.
// Returns nil if no depot auth is configured.
func GetDepotAuthConfig() *types.AuthConfig {
	// Try username/password environment variables first
	username := os.Getenv("DEPOT_PUSH_REGISTRY_USERNAME")
	registryPassword := os.Getenv("DEPOT_PUSH_REGISTRY_PASSWORD")

	if username != "" && registryPassword != "" {
		return &types.AuthConfig{
			Username: username,
			Password: registryPassword,
		}
	}

	// Try base64 encoded auth string
	auth := os.Getenv("DEPOT_PUSH_REGISTRY_AUTH")
	if auth != "" {
		decoded, err := base64.StdEncoding.DecodeString(auth)
		if err == nil {
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) == 2 && parts[0] != "" {
				return &types.AuthConfig{
					Username: parts[0],
					Password: parts[1],
				}
			}
		}
	}

	return nil
}
