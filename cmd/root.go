package cmd

import (
	"github.com/depot/cli/pkg/config"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:     "depot",
	Version: "0.0.0-dev",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Usage()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		panic(err)
	}
}

func init() {
	rootCmd.SetVersionTemplate("{{.Version}}")
	config.NewConfig()
}
