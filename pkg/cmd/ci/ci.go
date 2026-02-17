package ci

import "github.com/spf13/cobra"

func NewCmdCI() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Manage Depot CI",
		Long:  "Manage Depot CI workflows, secrets, and variables.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCmdMigrate())
	cmd.AddCommand(NewCmdSecrets())
	cmd.AddCommand(NewCmdVars())
	cmd.AddCommand(NewCmdStatus())
	cmd.AddCommand(NewCmdLogs())

	return cmd
}
