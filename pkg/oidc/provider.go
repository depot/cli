package oidc

import "context"

const audience = "depot.dev"

type OIDCProvider interface {
	RetrieveToken(ctx context.Context) (string, error)
}

var Providers = []OIDCProvider{
	NewGitHubOIDCProvider(),
	NewBuildkiteOIDCProvider(),
}
