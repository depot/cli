package helpers

import (
	"os"
	"path/filepath"

	"github.com/depot/cli/pkg/project"
)

func ResolveProjectID(id string, cwd string) string {
	if id != "" {
		return id
	}

	id = os.Getenv("DEPOT_PROJECT_ID")

	if id == "" {
		cwd, _ := filepath.Abs(cwd)
		config, _, err := project.ReadConfig(cwd)
		if err == nil {
			id = config.ID
		}
	}

	return id
}
