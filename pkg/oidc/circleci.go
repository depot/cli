package oidc

import (
	"context"
	"os"
)

type CircleCIOIDCProvider struct {
}

func NewCircleCIOIDCProvider() *CircleCIOIDCProvider {
	return &CircleCIOIDCProvider{}
}

func (p *CircleCIOIDCProvider) Name() string {
	return "circleci"
}

func (p *CircleCIOIDCProvider) RetrieveToken(ctx context.Context) (string, error) {
	token := os.Getenv("CIRCLE_OIDC_TOKEN_V2")
	return token, nil
}
