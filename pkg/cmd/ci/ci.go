package ci

import "github.com/spf13/cobra"

func NewCmdCI() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Manage Depot CI [beta]",
		Long:  "Manage Depot CI workflows, secrets, and variables.\n\nThis command is in beta and subject to change.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCmdCancel())
	cmd.AddCommand(NewCmdDispatch())
	cmd.AddCommand(NewCmdLogs())
	cmd.AddCommand(NewCmdMigrate())
	cmd.AddCommand(NewCmdRerun())
	cmd.AddCommand(NewCmdRetry())
	cmd.AddCommand(NewCmdRun())
	cmd.AddCommand(NewCmdSecrets())
	cmd.AddCommand(NewCmdSSH())
	cmd.AddCommand(NewCmdStatus())
	cmd.AddCommand(NewCmdVars())

	return cmd
}
