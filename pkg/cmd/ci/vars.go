package ci

import "github.com/spf13/cobra"

func NewCmdVars() *cobra.Command {
	return &cobra.Command{
		Use:   "vars",
		Short: "Manage CI variables",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
}
