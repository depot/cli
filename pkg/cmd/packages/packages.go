package packages

import (
	"github.com/spf13/cobra"
)

func NewCmdPackages() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "packages",
		Short: "Configure package managers to use Depot Packages",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCmdConfigure())
	cmd.AddCommand(NewCmdToken())

	return cmd
}
