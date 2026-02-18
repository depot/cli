package ci

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewCmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show CI workflow status [beta]",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Depot CI status is not yet available. Coming soon!")
			return nil
		},
	}
}
