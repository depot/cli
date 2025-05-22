package helpers

import (
	"os"
	"runtime"
)

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

// IsDepotGitHubActionsRunner detects Depot runners by checking for agentd binary in OS-specific locations.
func IsDepotGitHubActionsRunner() bool {
	var agentdPaths []string

	switch runtime.GOOS {
	case "windows":
		agentdPaths = []string{
			"C:\\ProgramData\\Agentd\\agentd-service.exe",
		}
	case "darwin":
		agentdPaths = []string{
			"/usr/local/bin/agentd",
		}
	case "linux":
		agentdPaths = []string{
			"/usr/local/bin/agentd",
		}
	default:
		agentdPaths = []string{
			"/usr/local/bin/agentd",
		}
	}

	for _, path := range agentdPaths {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}

	return false
}
