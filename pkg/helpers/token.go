package helpers

import (
	"context"
	"fmt"
	"os"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/oidc"
	"github.com/depot/cli/pkg/runtoken"
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

// ResolveCacheAuth is ResolveOrgAuth for cache tools, which keep a cache token over a run token that expires mid-build.
func ResolveCacheAuth(ctx context.Context, tok string) (string, error) {
	token, err := ResolveOrgAuth(ctx, tok)
	if source := runTokenSource(); err == nil && source != nil && source.Minted(token) {
		if cacheToken := resolveJITToken(); cacheToken != "" {
			return cacheToken, nil
		}
	}
	return token, err
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

	if token := resolveRunToken(context.Background()); token != "" {
		return token
	}

	return resolveJITToken()
}

func ResolveProjectAuth(ctx context.Context, tok string) (string, error) {
	if token := resolveStaticProjectAuth(tok); token != "" {
		return token, nil
	}

	if token := resolveRunToken(ctx); token != "" {
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

	intentID := oidc.SecretMigrationIntentIDFromGitHubActionsEnvironment()
	if intentID != "" {
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

var runTokenSource = runtoken.Default

// resolveRunToken mints a managed run's own token; the API client refreshes it per request.
func resolveRunToken(ctx context.Context) string {
	source := runTokenSource()
	if source == nil {
		return ""
	}
	token, err := source.Token(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		return ""
	}
	return token
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
