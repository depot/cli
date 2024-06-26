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
		var err error
		debug := os.Getenv("DEPOT_DEBUG_OIDC") != ""

		for _, provider := range oidc.Providers {
			if debug {
				fmt.Printf("Trying OIDC provider %s\n", provider.Name())
			}

			token, err = provider.RetrieveToken(ctx)

			if err != nil && debug {
				fmt.Printf("OIDC provider %s failed: %v\n", provider.Name(), err)
			}

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
