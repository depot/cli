package helpers

import (
	"fmt"
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
	
	// Debug: Print detection start
	if os.Getenv("DEPOT_DEBUG_DETECTION") != "" {
		fmt.Fprintf(os.Stderr, "[DEPOT DEBUG] Starting Depot GHA runner detection on %s/%s\n", runtime.GOOS, runtime.GOARCH)
	}

	switch runtime.GOOS {
	case "windows":
		agentdPaths = []string{
			"C:\\ProgramData\\Agentd\\agentd-service.exe", // Actual Windows installation path
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
		if os.Getenv("DEPOT_DEBUG_DETECTION") != "" {
			fmt.Fprintf(os.Stderr, "[DEPOT DEBUG] Checking for agentd at: %s\n", path)
		}
		
		if _, err := os.Stat(path); err == nil {
			if os.Getenv("DEPOT_DEBUG_DETECTION") != "" {
				fmt.Fprintf(os.Stderr, "[DEPOT DEBUG] Found agentd at %s - Depot runner DETECTED\n", path)
			}
			return true
		} else if os.Getenv("DEPOT_DEBUG_DETECTION") != "" {
			fmt.Fprintf(os.Stderr, "[DEPOT DEBUG] agentd not found at %s: %v\n", path, err)
		}
	}
	
	if os.Getenv("DEPOT_DEBUG_DETECTION") != "" {
		fmt.Fprintf(os.Stderr, "[DEPOT DEBUG] No agentd found - NOT a Depot runner\n")
	}

	return false
}
