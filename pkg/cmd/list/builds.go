// Lists the latests builds for a project.
package list

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdBuilds() *cobra.Command {
	var projectID string
	var token string
	var outputFormat string

	cmd := &cobra.Command{
		Use:     "builds",
		Aliases: []string{"b"},
		Short:   "List builds for a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			projectID := helpers.ResolveProjectID(projectID, cwd)
			if projectID == "" {
				return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
			}

			token, err := helpers.ResolveToken(context.Background(), token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			client := api.NewBuildClient()
			if !helpers.IsTerminal() && outputFormat == "" {
				outputFormat = "csv"
			}
			if outputFormat != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				depotBuilds, err := helpers.Builds(ctx, token, projectID, client)
				if err != nil {
					return err
				}

				switch outputFormat {
				case "csv":
					return depotBuilds.WriteCSV()
				case "json":
					return depotBuilds.WriteJSON()
				}

				return errors.Errorf("unknown format: %s. Requires csv or json", outputFormat)
			}

			m := helpers.NewBuildsModel(projectID, token, client)
			_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
			return err
		},
	}

	flags := cmd.Flags()

	flags.StringVar(&projectID, "project", "", "Depot project ID")
	flags.StringVar(&token, "token", "", "Depot token")
	flags.StringVar(&outputFormat, "output", "", "Non-interactive output format (json, csv)")

	return cmd
}
