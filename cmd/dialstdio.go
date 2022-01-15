package cmd

import (
	"github.com/depot/cli/pkg/builder"
	"github.com/spf13/cobra"
)

var dialStdioCommand = &cobra.Command{
	Use:    "dial-stdio",
	Hidden: true,
	Run: func(cmd *cobra.Command, args []string) {
		apiKey := "xxx"
		builderID := "healthz"

		err := builder.NewProxy(apiKey, builderID)
		if err != nil {
			panic(err)
		}
	},
}

func init() {
	rootCmd.AddCommand(dialStdioCommand)
}
