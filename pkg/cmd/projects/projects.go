package projects

import (
	"github.com/depot/cli/pkg/cmd/list"
	"github.com/spf13/cobra"
)

func NewCmdProjects() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"p"},
		Short:   "Create or display depot project information",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCmdCreate())
	cmd.AddCommand(list.NewCmdProjects("list", "ls"))

	return cmd
}
