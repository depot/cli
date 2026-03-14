package helpers

import (
	"context"
	"fmt"
	"os"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/oidc"
)

// debugAuth returns true if DEPOT_DEBUG_AUTH is set for verbose auth logging.
func debugAuth() bool {
	return os.Getenv("DEPOT_DEBUG_AUTH") != ""
}

// logAuthDebug prints debug info to stderr if DEPOT_DEBUG_AUTH is set.
func logAuthDebug(format string, args ...interface{}) {
	if debugAuth() {
		fmt.Fprintf(os.Stderr, "[DEBUG AUTH] "+format+"\n", args...)
	}
}

// maskToken returns a masked version of a token for safe logging.
func maskToken(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

func ResolveOrgAuth(ctx context.Context, tok string) (string, error) {
	logAuthDebug("ResolveOrgAuth starting")

	if tok != "" {
		logAuthDebug("Using explicit token argument: %s", maskToken(tok))
		return tok, nil
	}

	if token := os.Getenv("DEPOT_TOKEN"); token != "" {
		logAuthDebug("Using DEPOT_TOKEN environment variable: %s", maskToken(token))
		return token, nil
	}

	if token := config.GetApiToken(); token != "" {
		logAuthDebug("Using token from config file (~/.config/depot/depot.yaml): %s", maskToken(token))
		return token, nil
	}

	if token := resolveJITToken(); token != "" {
		logAuthDebug("Using JIT token: %s", maskToken(token))
		return token, nil
	}

	if IsTerminal() {
		logAuthDebug("No token found, initiating device authorization")
		return authorizeDevice(ctx)
	}

	logAuthDebug("No token found and not a terminal, returning empty")
	return "", nil
}

func ResolveProjectAuth(ctx context.Context, tok string) (string, error) {
	logAuthDebug("ResolveProjectAuth starting")

	if tok != "" {
		logAuthDebug("Using explicit token argument: %s", maskToken(tok))
		return tok, nil
	}

	if token := os.Getenv("DEPOT_TOKEN"); token != "" {
		logAuthDebug("Using DEPOT_TOKEN environment variable: %s", maskToken(token))
		return token, nil
	}

	if token := config.GetApiToken(); token != "" {
		logAuthDebug("Using token from config file (~/.config/depot/depot.yaml): %s", maskToken(token))
		return token, nil
	}

	if token := resolveOIDCToken(ctx); token != "" {
		logAuthDebug("Using OIDC token: %s", maskToken(token))
		return token, nil
	}

	if token := resolveJITToken(); token != "" {
		logAuthDebug("Using JIT token: %s", maskToken(token))
		return token, nil
	}

	if IsTerminal() {
		logAuthDebug("No token found, initiating device authorization")
		return authorizeDevice(ctx)
	}

	logAuthDebug("No token found and not a terminal, returning empty")
	return "", nil
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
	debug := os.Getenv("DEPOT_DEBUG_OIDC") != "" || debugAuth()

	logAuthDebug("Checking OIDC providers...")

	for _, provider := range oidc.Providers {
		if debug {
			fmt.Fprintf(os.Stderr, "[DEBUG OIDC] Trying OIDC provider %s\n", provider.Name())
		}

		token, err := provider.RetrieveToken(ctx)

		if err != nil && debug {
			fmt.Fprintf(os.Stderr, "[DEBUG OIDC] OIDC provider %s failed: %v\n", provider.Name(), err)
		}

		if token != "" {
			if debug {
				fmt.Fprintf(os.Stderr, "[DEBUG OIDC] Got token from provider %s: %s\n", provider.Name(), maskToken(token))
			}
			return token
		}
	}

	logAuthDebug("No OIDC token found from any provider")
	return ""
}

func resolveJITToken() string {
	logAuthDebug("Checking JIT tokens...")

	if token := os.Getenv("DEPOT_JIT_TOKEN"); token != "" {
		logAuthDebug("Found DEPOT_JIT_TOKEN: %s", maskToken(token))
		return token
	}

	if token := os.Getenv("DEPOT_CACHE_TOKEN"); token != "" {
		logAuthDebug("Found DEPOT_CACHE_TOKEN: %s", maskToken(token))
		return token
	}

	logAuthDebug("No JIT token found")
	return ""
}
