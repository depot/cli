package helpers

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

func ResolveBuildPlatform(buildPlatform string) (string, error) {
	if buildPlatform == "" {
		buildPlatform = os.Getenv("DEPOT_BUILD_PLATFORM")
	}

	if buildPlatform == "" {
		buildPlatform = "dynamic"
	}

	if buildPlatform != "linux/amd64" && buildPlatform != "linux/arm64" && buildPlatform != "dynamic" {
		return "", fmt.Errorf("invalid build platform: %s (must be one of: dynamic, linux/amd64, linux/arm64)", buildPlatform)
	}

	return buildPlatform, nil
}

func ResolveMachinePlatform(platform string) (string, error) {
	if platform == "" {
		platform = os.Getenv("DEPOT_BUILD_PLATFORM")
	}

	switch platform {
	case "linux/arm64":
		platform = "arm64"
	case "linux/amd64":
		platform = "amd64"
	case "":
		if strings.HasPrefix(runtime.GOARCH, "arm") {
			platform = "arm64"
		} else {
			platform = "amd64"
		}
	default:
		return "", fmt.Errorf("invalid platform: %s (must be one of: amd64, arm64)", platform)
	}

	return platform, nil
}
