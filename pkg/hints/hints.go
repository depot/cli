package hints

import (
	"os"
	"path/filepath"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/project"
)

func SendBuildHint() {
	projectID := os.Getenv("DEPOT_PROJECT_ID")
	if projectID == "" {
		cwd, _ := filepath.Abs(".")
		config, _, err := project.ReadConfig(cwd)
		if err == nil {
			projectID = config.ID
		}
	}
	if projectID == "" {
		return
	}

	token := os.Getenv("DEPOT_TOKEN")
	if token == "" {
		token = config.GetApiToken()
	}
	if token == "" {
		return
	}

	client, err := api.NewDepotFromEnv(token)
	if err != nil {
		return
	}

	_, _ = client.ReportBuildHint(projectID)
}
