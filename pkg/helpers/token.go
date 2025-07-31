package helpers

import (
	"context"
	"fmt"
	"os"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/oidc"
)

func ResolveProjectAuth(ctx context.Context, tok string) (string, error) {
	var token string

	if tok != "" {
		return tok, nil
	}

	if token := os.Getenv("DEPOT_TOKEN"); token != "" {
		return token, nil
	}

	if token := config.GetApiToken(); token != "" {
		return token, nil
	}

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
