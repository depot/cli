package helpers

import (
	"fmt"
	"os"
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
