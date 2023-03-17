package list

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewCmdList() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Display depot project and build information",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("missing subcommand, please run `depot list --help`")
		},
	}

	cmd.AddCommand(NewCmdProjects())
	cmd.AddCommand(NewCmdBuilds())

	return cmd
}
