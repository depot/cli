package sandbox

import (
	"github.com/spf13/cobra"
)

// NewCmdSandbox wires the `depot sandbox` verb tree.
//
// Vertical-slice subset of the M34 "CLI v2 catches up to SDK v0" surface:
// the new depot.sandbox.v1 verbs (create / exec / stop / kill) ship in this
// PR alongside the pre-existing legacy verbs (exec-pipe / pty) which target
// CI-bastion sandboxes via depot.ci.v1. The remaining v0 surface
// (get / list / from-spec / shell / logs / fs / snapshot / build) lands in
// follow-ons.
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
  exec-pipe     Execute a command and pipe local stdin (legacy CI-bastion).
  pty           Open a pseudo-terminal (legacy CI-bastion).`,
	}

	cmd.PersistentFlags().String("token", "", "Depot API token")
	cmd.PersistentFlags().String("org", "", "Organization ID (required when user is a member of multiple organizations)")

	// Lifecycle (depot.sandbox.v1)
	cmd.AddCommand(newSandboxCreate())
	cmd.AddCommand(newSandboxStop())
	cmd.AddCommand(newSandboxKill())

	// Command surface
	cmd.AddCommand(newSandboxExec())

	// Legacy CI-bastion verbs (depot.ci.v1)
	cmd.AddCommand(newSandboxExecPipe())
	cmd.AddCommand(newSandboxPty())

	return cmd
}
