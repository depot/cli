package sandbox

import (
	"github.com/spf13/cobra"
)

func NewCmdSandbox() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox [flags] [compute args...]",
		Short: "Manage Depot compute",
		Long: `Manage Depot sandboxes — declaratively configured VMs.

Lifecycle (sandbox.depot.yml + SandboxService):
  init           Scaffold a sandbox.depot.yml.
  up             Start a sandbox from a sandbox.depot.yml.
  ls             List sandboxes for the current organization.
  shell          Open an interactive shell in a running sandbox.
  cp             Copy files between local filesystem and a sandbox.
  snapshot       Snapshot a running sandbox to a registry image.
  logs           Stream entrypoint stdout/stderr from a sandbox.
  kill           Terminate one or more sandboxes.

Direct exec (skip the spec, run a command in an existing sandbox):
  exec           Execute arbitrary commands within the sandbox.
  exec-pipe      Execute a command and stream bytes to its stdin.`,
	}

	cmd.PersistentFlags().String("token", "", "Depot API token")
	cmd.PersistentFlags().String("org", "", "Organization ID (required when user is a member of multiple organizations)")

	cmd.AddCommand(newSandboxInit())
	cmd.AddCommand(newSandboxUp())
	cmd.AddCommand(newSandboxBuild())
	cmd.AddCommand(newSandboxList())
	cmd.AddCommand(newSandboxShell())
	cmd.AddCommand(newSandboxCp())
	cmd.AddCommand(newSandboxSnapshot())
	cmd.AddCommand(newSandboxLogs())
	cmd.AddCommand(newSandboxKill())
	cmd.AddCommand(newSandboxExec())
	cmd.AddCommand(newSandboxExecPipe())

	return cmd
}
