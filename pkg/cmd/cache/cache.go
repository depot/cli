package init

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewCmdCache() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Operations for the Depot project cache",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("missing subcommand, please run `depot cache --help`")
		},
	}

	cmd.AddCommand(NewCmdResetCache())

	return cmd
}
