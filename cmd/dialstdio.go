package cmd

import (
	"fmt"

	"github.com/depot/cli/pkg/builder"
	"github.com/spf13/cobra"
)

var dialStdioCommand = &cobra.Command{
	Use:    "dial-stdio",
	Hidden: true,
	Run: func(cmd *cobra.Command, args []string) {
		apiKey := "xxx"

		err := builder.NewProxy(apiKey)
		fmt.Printf("%v\n", err)
		if err != nil {
			panic(err)
		}
	},
}

func init() {
	rootCmd.AddCommand(dialStdioCommand)
}
