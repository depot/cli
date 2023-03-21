package helpers

import (
	"context"
	"os"

	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/oidc"
)

func ResolveToken(ctx context.Context, token string) string {
	if token == "" {
		token = os.Getenv("DEPOT_TOKEN")
	}

	if token == "" {
		token = config.GetApiToken()
	}

	if os.Getenv("DEPOT_EXPERIMENTAL_OIDC") != "" && token == "" {
		for _, provider := range oidc.Providers {
			token, _ = provider.RetrieveToken(ctx)
			if token != "" {
				return token
			}
		}
	}

	return token
}
