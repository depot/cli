package org

import "github.com/spf13/cobra"

func NewCmdOrg() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "org",
		Aliases: []string{"o"},
		Short:   "Organization management",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCmdList())
	cmd.AddCommand(NewCmdSwitch())
	cmd.AddCommand(NewCmdShow())

	return cmd
}
