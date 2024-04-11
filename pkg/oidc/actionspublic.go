package oidc

import (
	"context"

	"github.com/depot/cli/pkg/oidc/actionspublic"
)

type ActionsPublicProvider struct {
}

func NewActionsPublicProvider() *ActionsPublicProvider {
	return &ActionsPublicProvider{}
}

func (p *ActionsPublicProvider) Name() string {
	return "actions-public"
}

func (p *ActionsPublicProvider) RetrieveToken(ctx context.Context) (string, error) {
	token, err := actionspublic.RetrieveToken(ctx, "https://depot.dev")
	return token, err
}
