package sandbox

import (
	"github.com/spf13/cobra"
)

func NewCmdSandbox() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox [flags] [compute args...]",
		Short: "Manage Depot compute",
		Long: `Compute is the building block of Depot CI. 

The command enables user to start, stop and interact with compute instances.

Subcommands:
  exec           Execute arbitrary commands within the compute. Streams stdout, stderr and exit code.
  exec-pipe      Execute a command, then stream bytes to the command's stdin.
  pty            Open a pseudo-terminal within the compute.`,
	}

	cmd.PersistentFlags().String("token", "", "Depot API token")

	cmd.AddCommand(newSandboxExec())
	cmd.AddCommand(newSandboxPty())
	cmd.AddCommand(newSandboxExecPipe())

	return cmd
}
