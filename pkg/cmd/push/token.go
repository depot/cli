package push

import (
	"context"
	"net/http"

	"github.com/containerd/containerd/remotes/docker/auth"
	remoteerrors "github.com/containerd/containerd/remotes/errors"
	configtypes "github.com/docker/cli/cli/config/types"
	"github.com/pkg/errors"
)

type Token struct {
	Token  string
	Scheme string
}

// FetchToken gets a token for the registry using the realm and scoped by scopes.
// This token is used as the bearer token for the registry.
func FetchToken(ctx context.Context, config *configtypes.AuthConfig, challenge *auth.Challenge, scopes []string) (*Token, error) {
	// This checks for static token in config.json.
	token := RegistryToken(config)
	if token != nil {
		return token, nil
	}

	var (
		username string
		secret   string
	)
	if config.IdentityToken != "" {
		secret = config.IdentityToken
	} else {
		username = config.Username
		secret = config.Password
	}

	if secret == "" {
		return GetAnonymousToken(ctx, username, challenge, scopes)
	}

	return GetOAuthToken(ctx, username, secret, challenge, scopes)
}

// RegistryToken is a static token for the registry that is defined in the config.json.
func RegistryToken(authConfig *configtypes.AuthConfig) *Token {
	if authConfig.RegistryToken == "" {
		return nil
	}

	return &Token{
		Token: authConfig.RegistryToken,
		// As far as I can tell, this is supposed to be bearer.
		Scheme: "Bearer",
	}
}

// GetAnonymousToken gets a token when the registry does not require authentication.
// I'm not sure when this is used.
func GetAnonymousToken(ctx context.Context, username string, challenge *auth.Challenge, scopes []string) (*Token, error) {
	realm := challenge.Parameters["realm"]
	service := challenge.Parameters["service"]

	tokenOptions := auth.TokenOptions{
		Realm:    realm,
		Service:  service,
		Scopes:   scopes,
		Username: username,
	}
	client := http.DefaultClient
	var headers http.Header
	// TODO: handle nil fetchtoken
	res, err := auth.FetchToken(ctx, client, headers, tokenOptions)
	if err != nil {
		return nil, err
	}

	return &Token{
		Token:  res.Token,
		Scheme: scheme(challenge.Scheme),
	}, nil
}

// GetOAuthToken gets a token when the registry requires authentication.
func GetOAuthToken(ctx context.Context, username, secret string, challenge *auth.Challenge, scopes []string) (*Token, error) {
	realm := challenge.Parameters["realm"]
	service := challenge.Parameters["service"]

	tokenOptions := auth.TokenOptions{
		Realm:    realm,
		Service:  service,
		Scopes:   scopes,
		Username: username,
		Secret:   secret,
	}

	client := http.DefaultClient
	var headers http.Header
	// TODO: handle nil fetchtoken
	res, err := auth.FetchTokenWithOAuth(ctx, client, headers, "depot-client", tokenOptions)
	if err == nil {
		return &Token{
			Token:  res.AccessToken,
			Scheme: scheme(challenge.Scheme),
		}, nil
	}

	var errStatus remoteerrors.ErrUnexpectedStatus
	if !errors.As(err, &errStatus) {
		return nil, err
	}

	// Registries without support for POST may return 404 for POST /v2/token.
	// As of September 2017, GCR is known to return 404.
	// As of February 2018, JFrog Artifactory is known to return 401.
	// As of January 2022, ACR is known to return 400
	//
	// DEPOT: this is the fallback to GET /v2/token.
	getRes, err := auth.FetchToken(ctx, client, headers, tokenOptions)
	if err != nil {
		return nil, err
	}

	return &Token{
		Token:  getRes.Token,
		Scheme: scheme(challenge.Scheme),
	}, nil
}

func scheme(scheme auth.AuthenticationScheme) string {
	switch scheme {
	case auth.BasicAuth:
		return "Basic"
	case auth.DigestAuth:
		return "Digest"
	case auth.BearerAuth:
		return "Bearer"
	default:
		return ""
	}
}
