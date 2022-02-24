package cmd

import (
	"fmt"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/spf13/cobra"
)

var loginCommand = &cobra.Command{
	Use: "login",
	RunE: func(cmd *cobra.Command, args []string) error {
		clear, _ := cmd.Flags().GetBool("clear")

		if clear {
			config.ClearApiToken()
		}

		existingToken := config.GetApiToken()
		if existingToken != "" {
			fmt.Println("You are already logged in.")
			return nil
		}

		depot, err := api.NewDepotFromEnv()
		if err != nil {
			return err
		}

		tokenResponse, err := depot.AuthorizeDevice()
		if err != nil {
			return err
		}

		fmt.Println("Successfully authenticated!")

		err = config.SetApiToken(tokenResponse.Token)
		if err != nil {
			return err
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(loginCommand)
	loginCommand.Flags().Bool("clear", false, "Clear any existing token before logging in")
}
