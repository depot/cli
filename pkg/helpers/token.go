package helpers

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/oidc"
)

func ResolveOrgAuth(ctx context.Context, tok string) (string, error) {
	if token := ResolveStaticOrgAuth(tok); token != "" {
		return token, nil
	}

	if IsTerminal() {
		return authorizeDevice(ctx)
	}

	return "", nil
}

func ResolveStaticOrgAuth(tok string) string {
	if tok != "" {
		return tok
	}

	if token := os.Getenv("DEPOT_TOKEN"); token != "" {
		return token
	}

	if token := config.GetApiToken(); token != "" {
		return token
	}

	return resolveJITToken()
}

func ResolveProjectAuth(ctx context.Context, tok string) (string, error) {
	if token := resolveStaticProjectAuth(tok); token != "" {
		return token, nil
	}

	if token := resolveOIDCToken(ctx); token != "" {
		return token, nil
	}

	if token := resolveJITToken(); token != "" {
		return token, nil
	}

	if IsTerminal() {
		return authorizeDevice(ctx)
	}

	return "", nil
}

func ResolveProjectAuthForSecretMigration(ctx context.Context, tok string) (string, error) {
	if token := resolveStaticProjectAuth(tok); token != "" {
		return token, nil
	}

	intentID := strings.TrimSpace(os.Getenv(oidc.SecretMigrationIntentIDEnv))
	if intentID != "" && os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN") != "" && os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL") != "" {
		token, err := oidc.NewGitHubOIDCProvider().RetrieveSecretMigrationToken(ctx, intentID)
		if err != nil {
			return "", err
		}
		if token != "" {
			return token, nil
		}
	}

	return ResolveProjectAuth(ctx, tok)
}

func resolveStaticProjectAuth(tok string) string {
	if tok != "" {
		return tok
	}

	if token := os.Getenv("DEPOT_TOKEN"); token != "" {
		return token
	}

	return config.GetApiToken()
}

func authorizeDevice(ctx context.Context) (string, error) {
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

func resolveOIDCToken(ctx context.Context) string {
	debug := os.Getenv("DEPOT_DEBUG_OIDC") != ""

	for _, provider := range oidc.Providers {
		if debug {
			fmt.Printf("Trying OIDC provider %s\n", provider.Name())
		}

		token, err := provider.RetrieveToken(ctx)

		if err != nil && debug {
			fmt.Printf("OIDC provider %s failed: %v\n", provider.Name(), err)
		}

		if token != "" {
			return token
		}
	}

	return ""
}

func resolveJITToken() string {
	if token := os.Getenv("DEPOT_JIT_TOKEN"); token != "" {
		return token
	}

	if token := os.Getenv("DEPOT_CACHE_TOKEN"); token != "" {
		return token
	}

	return ""
}
