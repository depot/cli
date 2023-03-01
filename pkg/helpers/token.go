package helpers

import (
	"os"

	"github.com/depot/cli/pkg/config"
)

func ResolveToken(token string) string {
	if token == "" {
		token = os.Getenv("DEPOT_TOKEN")
	}
	if token == "" {
		token = config.GetApiToken()
	}
	return token
}
