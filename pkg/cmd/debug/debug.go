package debug

import (
	"fmt"

	"github.com/spf13/cobra"
)

// TODO: make this be `depot debug workers`
func NewCmdDebug() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "debug",
		Hidden: true,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("ID       PLATFORMS")
			fmt.Println("depot    linux/amd64")
		},
	}
	return cmd
}
