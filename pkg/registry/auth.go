package registry

import (
	"context"
	"os"

	"github.com/depot/cli/pkg/build"
	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/auth/authprovider"
	"google.golang.org/grpc"
)

var (
	_ auth.AuthServer    = (*AuthProvider)(nil)
	_ session.Attachable = (*AuthProvider)(nil)
)

type AuthProvider struct {
	inner auth.AuthServer
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
				inner: p.(auth.AuthServer),
			}
		}
	}

	return as
}

func (a *AuthProvider) Register(server *grpc.Server) {
	auth.RegisterAuthServer(server, a)
}

func (a *AuthProvider) Credentials(ctx context.Context, req *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	return a.inner.Credentials(ctx, req)
}

func (a *AuthProvider) FetchToken(ctx context.Context, req *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	return a.inner.FetchToken(ctx, req)
}

func (a *AuthProvider) GetTokenAuthority(ctx context.Context, req *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	return a.inner.GetTokenAuthority(ctx, req)
}

func (a *AuthProvider) VerifyTokenAuthority(ctx context.Context, req *auth.VerifyTokenAuthorityRequest) (*auth.VerifyTokenAuthorityResponse, error) {
	return a.inner.VerifyTokenAuthority(ctx, req)
}
