package cmd

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use: "depot",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Usage()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		panic(err)
	}
}
