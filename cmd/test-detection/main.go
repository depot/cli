package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/depot/cli/pkg/helpers"
)

func main() {
	fmt.Println("=== Depot GHA Detection Test ===")
	fmt.Printf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	
	hostname, err := os.Hostname()
	if err != nil {
		fmt.Printf("Hostname: <error: %v>\n", err)
	} else {
		fmt.Printf("Hostname: %s\n", hostname)
	}
	
	// Enable debug mode
	os.Setenv("DEPOT_DEBUG_DETECTION", "1")
	
	fmt.Println("\nRunning detection...")
	isDepot := helpers.IsDepotGitHubActionsRunner()
	
	fmt.Printf("\nResult: ")
	if isDepot {
		fmt.Println("✓ DEPOT RUNNER DETECTED")
	} else {
		fmt.Println("✗ NOT a Depot runner")
	}
	
	// Also check specific paths manually for verification
	fmt.Println("\nManual path checks:")
	
	var paths []string
	switch runtime.GOOS {
	case "windows":
		paths = []string{
			"C:\\ProgramData\\Agentd\\agentd-service.exe",
		}
	case "darwin":
		paths = []string{
			"/usr/local/bin/agentd",
		}
	default:
		paths = []string{
			"/usr/local/bin/agentd",
		}
	}
	
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("  ✓ Found: %s\n", path)
		} else {
			fmt.Printf("  ✗ Not found: %s\n", path)
		}
	}
}