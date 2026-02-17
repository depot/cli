package ci

import "github.com/spf13/cobra"

func NewCmdSecrets() *cobra.Command {
	return &cobra.Command{
		Use:   "secrets",
		Short: "Manage CI secrets",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
}
