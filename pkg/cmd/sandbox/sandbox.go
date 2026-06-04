package sandbox

import (
	"github.com/spf13/cobra"
)

// NewCmdSandbox wires up the `depot sandbox` verb tree.
//
// It registers the lifecycle and command verbs (create, exec, stop, kill)
// alongside the older CI-bastion verbs (exec-pipe, pty), which target
// CI-bastion sandboxes through a separate service.
func NewCmdSandbox() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox [flags]",
		Short: "Manage Depot sandboxes",
		Long: `Manage Depot sandboxes — declaratively configured VMs.

Lifecycle (depot.sandbox.v1):
  create        Create a new sandbox.
  stop          Gracefully stop a sandbox.
  kill          Force-terminate one or more sandboxes.

Command surface:
  exec          Execute a command within a running sandbox.
  snapshot      Snapshot a running sandbox to a registry image.
  exec-pipe     Execute a command and pipe local stdin (legacy CI-bastion).
  pty           Open a pseudo-terminal (legacy CI-bastion).`,
	}

	cmd.PersistentFlags().String("token", "", "Depot API token")
	cmd.PersistentFlags().String("org", "", "Organization ID (required when user is a member of multiple organizations)")

	// Lifecycle verbs.
	cmd.AddCommand(newSandboxCreate())
	cmd.AddCommand(newSandboxStop())
	cmd.AddCommand(newSandboxKill())

	// Command verbs.
	cmd.AddCommand(newSandboxExec())
	cmd.AddCommand(newSandboxSnapshot())

	// Legacy CI-bastion verbs.
	cmd.AddCommand(newSandboxExecPipe())
	cmd.AddCommand(newSandboxPty())

	return cmd
}
