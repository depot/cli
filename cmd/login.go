package cmd

import (
	"github.com/depot/cli/pkg/api"
	"github.com/spf13/cobra"
)

var loginCommand = &cobra.Command{
	Use: "login",
	RunE: func(cmd *cobra.Command, args []string) error {
		depot, err := api.NewDepotFromEnv()
		if err != nil {
			return err
		}

		err = depot.AuthorizeDevice()
		if err != nil {
			return err
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(loginCommand)
}
