package agent

import "github.com/spf13/cobra"

func NewCmdAgent() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "agent",
		Short:  "Run Depot agents",
		Hidden: true,
		RunE:   func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newLaunch())
	cmd.AddCommand(newSend())
	cmd.AddCommand(newCreate())
	cmd.AddCommand(newWatch())
	cmd.AddCommand(newLogs())
	cmd.AddCommand(newList())
	cmd.AddCommand(newStop())
	return cmd
}
