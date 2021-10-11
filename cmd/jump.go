package cmd

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
)

var jumpCommand = &cobra.Command{
	Use:    "jump",
	Hidden: true,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Waiting for connection...")

		shutdown := make(chan os.Signal, 1)
		signal.Notify(shutdown, os.Interrupt)

		<-shutdown

		fmt.Println("Closing...")
	},
}

func init() {
	rootCmd.AddCommand(jumpCommand)
}
