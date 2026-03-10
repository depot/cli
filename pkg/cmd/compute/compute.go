package compute

import (
	"github.com/spf13/cobra"
)

func NewCmdCompute() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compute [flags] [compute args...]",
		Short: "Manage Depot compute",
		Long: `Compute is the building block of Depot CI. 

The command enables user to start, stop and interact with compute instances.

Subcommands:
  exec           Execute arbitrary commands within the compute. Streams stdout, stderr and exit code.
  pty            Open a pseudo-terminal within the compute.`,
	}

	cmd.PersistentFlags().String("token", "", "Depot API token")

	cmd.AddCommand(newComputeExec())
	cmd.AddCommand(newComputePty())

	return cmd
}
