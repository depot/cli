package cmd

import (
	"fmt"
	"os"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/builder"
	"github.com/spf13/cobra"
)

var dialStdioCommand = &cobra.Command{
	Use:    "dial-stdio",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		depot, err := api.NewDepotFromEnv()
		if err != nil {
			return err
		}

		projectID := os.Getenv("DEPOT_PROJECT_ID")
		if projectID == "" {
			return fmt.Errorf("DEPOT_PROJECT_ID is not set")
		}

		build, err := depot.InitBuild(projectID)
		if err != nil {
			return err
		}

		if !build.OK {
			return fmt.Errorf("failed to init build")
		}

		// TODO: attempt to run this on CTRL+C
		// defer depot.FinishBuild(build.ID)

		err = builder.NewProxy(build.BaseURL, build.AccessToken, build.ID)
		if err != nil {
			return err
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(dialStdioCommand)
}
