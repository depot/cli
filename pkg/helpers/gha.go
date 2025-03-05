package helpers

import "os"

// If the CLI is running inside a Depot GitHub Actions runner, restore the original
// GitHub Actions cache URL so that the remote BuildKit doesn't attempt to use the internal cache.
func FixGitHubActionsCacheEnv() {
	original := os.Getenv("UPSTREAM_ACTIONS_CACHE_URL")

	if original != "" {
		os.Setenv("ACTIONS_CACHE_URL", original)
	}

	original = os.Getenv("UPSTREAM_ACTIONS_RESULTS_URL")

	if original != "" {
		os.Setenv("ACTIONS_RESULTS_URL", original)
	}
}
