package buildkitd

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func NewMockBuildkit() *cobra.Command {
	var cmd = &cobra.Command{
		Use:   "buildkitd <command> [flags]",
		Short: "Mock buildkitd for buildx container driver",
		Run: func(cmd *cobra.Command, args []string) {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
			<-sigCh
		},
	}

	cmd.Version = "buildkitd github.com/moby/buildkit v0.11.6 2951a28cd7085eb18979b1f710678623d94ed578"

	return cmd
}
