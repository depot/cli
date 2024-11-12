package build

import (
	"github.com/depot/cli/pkg/buildx/commands"
	_ "github.com/depot/cli/pkg/buildxdriver"
	"github.com/spf13/cobra"
)

func NewCmdBuild() *cobra.Command {
	return commands.BuildCmd()
}
