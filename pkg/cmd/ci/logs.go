package ci

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewCmdLogs() *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "View CI workflow logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Depot CI logs is not yet available. Coming soon!")
			return nil
		},
	}
}
