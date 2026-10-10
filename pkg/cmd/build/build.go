package build

import (
	"github.com/depot/cli/pkg/buildx/commands"
	"github.com/spf13/cobra"
)

func NewCmdBuild() *cobra.Command {
	return commands.BuildCmd()
}
