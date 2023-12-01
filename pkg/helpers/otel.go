package helpers

import (
	"os"
	"strings"
)

func DisableOTEL() {
	vars := os.Environ()
	for _, env := range vars {
		parts := strings.Split(env, "=")
		if len(parts) == 0 {
			continue
		}

		if strings.Contains(parts[0], "OTEL") {
			os.Unsetenv(parts[0])
		}
	}
}
