package agent

import "github.com/spf13/cobra"

func NewCmdAgent() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run Depot agents",
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newLaunch())
	return cmd
}
