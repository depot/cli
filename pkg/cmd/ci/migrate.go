package ci

import "github.com/spf13/cobra"

func NewCmdMigrate() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Migrate GitHub Actions workflows to Depot CI",
		Long:  "Interactive wizard to migrate your GitHub Actions CI configuration to Depot CI.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
}
