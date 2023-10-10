package helpers

import (
	"context"
	"fmt"
	"os"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/oidc"
)

func ResolveToken(ctx context.Context, token string) (string, error) {
	if token == "" {
		token = os.Getenv("DEPOT_TOKEN")
	}

	if token == "" {
		token = config.GetApiToken()
	}

	if token == "" {
		for _, provider := range oidc.Providers {
			token, _ = provider.RetrieveToken(ctx)
			if token != "" {
				return token, nil
			}
		}
	}

	if token == "" && IsTerminal() {
		return AuthorizeDevice(ctx)
	}

	return token, nil
}

func AuthorizeDevice(ctx context.Context) (string, error) {
	tokenResponse, err := api.AuthorizeDevice(ctx)
	if err != nil {
		return "", err
	}

	fmt.Println("Successfully authenticated!")

	err = config.SetApiToken(tokenResponse.Token)
	if err != nil {
		return "", err
	}
	return tokenResponse.Token, nil
}
