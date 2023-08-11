package buildkitd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	depot "github.com/depot/cli/internal/build"
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

	cmd.SetVersionTemplate(`{{with .Name}}{{printf "%s github.com/depot/cli " .}}{{end}}{{printf "%s\n" .Version}}`)
	cmd.Version = fmt.Sprintf("%s 2951a28cd7085eb18979b1f710678623d94ed578", depot.Version)

	return cmd
}
