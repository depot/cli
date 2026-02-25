package registry

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/depot/cli/pkg/build"
	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/auth/authprovider"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	if dockerConfig.AuthConfigs == nil {
		dockerConfig.AuthConfigs = map[string]types.AuthConfig{}
	}

	for _, c := range credentials {
		authConfig := types.AuthConfig{
			Auth:          c.Token,
			ServerAddress: c.Host,
		}
		username, password, err := decodeCredentialToken(c.Token)
		if err == nil {
			authConfig.Username = username
			authConfig.Password = password
		}

		dockerConfig.AuthConfigs[c.Host] = authConfig
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
	cred, ok := a.credentialForHost(req.Host)
	if ok {
		username, password, err := decodeCredentialToken(cred.Token)
		if err != nil {
			return nil, err
		}

		return &auth.CredentialsResponse{
			Username: username,
			Secret:   password,
		}, nil
	}

	return a.inner.Credentials(ctx, req)
}

func (a *AuthProvider) FetchToken(ctx context.Context, req *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	return a.inner.FetchToken(ctx, req)
}

func (a *AuthProvider) GetTokenAuthority(ctx context.Context, req *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	if _, ok := a.credentialForHost(req.Host); ok {
		// Force BuildKit to fall back to the Credentials() flow for explicit
		// save credentials. This avoids credential-helper lookups that can
		// ignore in-memory auth configs.
		return nil, status.Error(codes.Unavailable, "token authority disabled for explicit credentials")
	}

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

func decodeCredentialToken(token string) (string, string, error) {
	decodedAuth, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return "", "", err
	}

	usernamePassword := strings.SplitN(string(decodedAuth), ":", 2)
	if len(usernamePassword) != 2 {
		return "", "", fmt.Errorf("invalid auth string")
	}

	return usernamePassword[0], usernamePassword[1], nil
}

func (a *AuthProvider) credentialForHost(host string) (build.Credential, bool) {
	for i := range a.credentials {
		if hostMatches(a.credentials[i].Host, host) {
			return a.credentials[i], true
		}
	}
	return build.Credential{}, false
}

func hostMatches(credentialHost, requestHost string) bool {
	if credentialHost == requestHost {
		return true
	}
	return strings.TrimSuffix(requestHost, ":443") == credentialHost
}
