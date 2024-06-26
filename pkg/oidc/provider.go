package oidc

import "context"

const audience = "https://depot.dev"

type OIDCProvider interface {
	Name() string
	RetrieveToken(ctx context.Context) (string, error)
}

var Providers = []OIDCProvider{
	NewGitHubOIDCProvider(),
	NewCircleCIOIDCProvider(),
	NewBuildkiteOIDCProvider(),
	NewActionsPublicProvider(),
}
