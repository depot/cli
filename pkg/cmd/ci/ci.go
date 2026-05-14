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

	cmd.AddCommand(NewCmdArtifacts())
	cmd.AddCommand(NewCmdCancel())
	cmd.AddCommand(NewCmdDiagnose())
	cmd.AddCommand(NewCmdDispatch())
	cmd.AddCommand(NewCmdLogs())
	cmd.AddCommand(NewCmdMetrics())
	cmd.AddCommand(NewCmdMigrate())
	cmd.AddCommand(NewCmdRerun())
	cmd.AddCommand(NewCmdRetry())
	cmd.AddCommand(NewCmdRun())
	cmd.AddCommand(NewCmdSecrets())
	cmd.AddCommand(NewCmdSSH())
	cmd.AddCommand(NewCmdStatus())
	cmd.AddCommand(NewCmdSummary())
	cmd.AddCommand(NewCmdVars())
	cmd.AddCommand(NewCmdWorkflow())

	return cmd
}
