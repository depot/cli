package sandbox

import (
	"github.com/spf13/cobra"
)

// NewCmdSandbox creates the parent sandbox command
func NewCmdSandbox() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage Depot sandboxes",
		Long: `Start, list, and connect to Depot sandbox environments.

Sandboxes are isolated compute environments that can be used for
development, testing, and interactive SSH sessions.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCmdStart())
	cmd.AddCommand(NewCmdConnect())
	cmd.AddCommand(NewCmdResume())
	cmd.AddCommand(NewCmdList())
	cmd.AddCommand(NewCmdKill())
	cmd.AddCommand(NewCmdTemplates())

	return cmd
}
