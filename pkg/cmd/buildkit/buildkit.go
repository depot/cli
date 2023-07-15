package buildkit

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func NewFakeBuildkit() *cobra.Command {
	var cmd = &cobra.Command{
		Use:          "buildkitd <command> [flags]",
		Short:        "Fake buildkitd for buildx container driver",
		Hidden:       true,
		SilenceUsage: true,

		Run: func(cmd *cobra.Command, args []string) {
			// TODO: log here about starting a depot process.
			// Perhaps it'll dump the env vars.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
			<-sigCh
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:    "debug",
		Short:  "Mimics buildctl debug workers",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	})

	cmd.Version = "buildkitd github.com/moby/buildkit v0.11.6 2951a28cd7085eb18979b1f710678623d94ed578"

	cmd.AddCommand(NewCmdDial())

	return cmd
}
