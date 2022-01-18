package cmd

import (
	"os"

	"github.com/depot/cli/pkg/builder"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var dialStdioCommand = &cobra.Command{
	Use:    "dial-stdio",
	Hidden: true,
	Run: func(cmd *cobra.Command, args []string) {
		apiHost := os.Getenv("DEPOT_API_HOST")
		apiKey := os.Getenv("DEPOT_API_KEY")
		builderID := os.Getenv("DEPOT_BUILDER_ID")

		if apiHost == "" {
			apiHost = "https://depot.dev"
		}

		if apiKey == "" {
			logrus.Fatalf("DEPOT_API_KEY env var is not set\n")
		}

		if builderID == "" {
			logrus.Fatalf("DEPOT_BUILDER_ID env var is not set\n")
		}

		err := builder.NewProxy(apiHost, apiKey, builderID)
		if err != nil {
			panic(err)
		}
	},
}

func init() {
	rootCmd.AddCommand(dialStdioCommand)
}
