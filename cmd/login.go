package cmd

import (
	"github.com/depot/cli/pkg/api"
	"github.com/spf13/cobra"
)

var loginCommand = &cobra.Command{
	Use: "login",
	Run: func(cmd *cobra.Command, args []string) {
		err := api.AuthorizeDevice()
		if err != nil {
			panic(err)
		}
	},
}

func init() {
	rootCmd.AddCommand(loginCommand)
}
