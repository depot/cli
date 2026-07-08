package tests

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/depot/cli/pkg/oidc"
)

const (
	depotCIOIDCRequestURL   = "http://169.254.169.253/token?v=1"
	depotCIOIDCRequestToken = "local"
	depotCIRunnerName       = "Depot CI"
)

var oidcDebugWriter io.Writer = os.Stderr

func resolveOIDCCredential(ctx context.Context) (string, error) {
	return resolveOIDCCredentialWithDepotCIEnv(ctx, oidc.Providers)
}

func resolveOIDCCredentialWithDepotCIEnv(ctx context.Context, providers []oidc.OIDCProvider) (string, error) {
	restoreEnv := configureDepotCIOIDCEnv()
	defer restoreEnv()
	return resolveOIDCCredentialWithProviders(ctx, providers)
}

func resolveOIDCCredentialWithProviders(ctx context.Context, providers []oidc.OIDCProvider) (string, error) {
	debug := os.Getenv("DEPOT_DEBUG_OIDC") != ""
	for _, provider := range providers {
		if debug {
			fmt.Fprintf(oidcDebugWriter, "Trying OIDC provider %s\n", provider.Name())
		}

		token, err := provider.RetrieveToken(ctx)
		if err != nil {
			if debug {
				fmt.Fprintf(oidcDebugWriter, "OIDC provider %s failed: %v\n", provider.Name(), err)
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", err
			}
			continue
		}
		if strings.TrimSpace(token) != "" {
			return strings.TrimSpace(token), nil
		}
	}
	return "", fmt.Errorf("missing OIDC credential; ensure this command is running in a supported CI environment with OIDC enabled")
}

func configureDepotCIOIDCEnv() func() {
	if !isRunningInDepotCI(os.Getenv) {
		return func() {}
	}
	restoreRequestURL := setEnvDefault("ACTIONS_ID_TOKEN_REQUEST_URL", depotCIOIDCRequestURL)
	restoreRequestToken := setEnvDefault("ACTIONS_ID_TOKEN_REQUEST_TOKEN", depotCIOIDCRequestToken)
	return func() {
		restoreRequestToken()
		restoreRequestURL()
	}
}

func isRunningInDepotCI(getenv func(string) string) bool {
	return getenv("GITHUB_ACTIONS") == "true" &&
		getenv("RUNNER_NAME") == depotCIRunnerName &&
		strings.TrimSpace(getenv("DEPOT_ORG_ID")) != ""
}

func setEnvDefault(key, value string) func() {
	previous, existed := os.LookupEnv(key)
	if strings.TrimSpace(previous) != "" {
		return func() {}
	}
	_ = os.Setenv(key, value)
	return func() {
		if existed {
			_ = os.Setenv(key, previous)
			return
		}
		_ = os.Unsetenv(key)
	}
}
