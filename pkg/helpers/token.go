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
	return resolveOrgAuth(ctx, tok, true)
}

func resolveOrgAuth(ctx context.Context, tok string, turn bool) (string, error) {
	if token := staticOrgAuth(tok, turn); token != "" {
		return token, nil
	}

	if IsTerminal() {
		return authorizeDevice(ctx)
	}

	return "", nil
}

// ResolveCacheAuth is ResolveOrgAuth for cache tools: a cache token over a run token that expires mid-build, never a turn's token.
func ResolveCacheAuth(ctx context.Context, tok string) (string, error) {
	token, err := resolveOrgAuth(ctx, tok, false)
	if source := runTokenSource(); err == nil && source != nil && source.Minted(token) {
		if cacheToken := resolveJITToken(); cacheToken != "" {
			return cacheToken, nil
		}
	}
	return token, err
}

// ResolveAgentAuth is ResolveOrgAuth for the session RPCs the API takes a Dispatch turn's own token on (DEP-8577):
// inside a turn that token comes before the cache token, and turn reports that it is the one returned.
func ResolveAgentAuth(ctx context.Context, tok string) (token string, turn bool, err error) {
	if token = resolveStaticProjectAuth(tok); token != "" {
		return token, false, nil
	}
	if token = resolveRunToken(ctx); token != "" {
		return token, false, nil
	}
	if token = mintToken(ctx, turnTokenSource()); token != "" {
		return token, true, nil
	}
	if token = resolveJITToken(); token != "" || !IsTerminal() {
		return token, false, nil
	}
	token, err = authorizeDevice(ctx)
	return token, false, err
}

// ResolveStaticOrgAuth sends a Dispatch turn's own token where it would otherwise send the sandbox's cache token,
// which the API takes on no call (DEP-8673).
func ResolveStaticOrgAuth(tok string) string {
	return staticOrgAuth(tok, true)
}

func staticOrgAuth(tok string, turn bool) string {
	if token := resolveStaticProjectAuth(tok); token != "" {
		return token
	}

	if token := resolveRunToken(context.Background()); token != "" {
		return token
	}

	if turn {
		if token := mintToken(context.Background(), turnTokenSource()); token != "" {
			return token
		}
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

var (
	runTokenSource  = runtoken.Default
	turnTokenSource = runtoken.Turn
)

// resolveRunToken mints a managed run's own token; the API client refreshes it per request.
func resolveRunToken(ctx context.Context) string {
	return mintToken(ctx, runTokenSource())
}

func mintToken(ctx context.Context, source *runtoken.Source) string {
	if source == nil {
		return ""
	}
	token, err := source.Token(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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
