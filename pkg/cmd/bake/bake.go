package init

import (
	"github.com/depot/cli/pkg/buildx/commands"
	"github.com/spf13/cobra"
)

func NewCmdBake() *cobra.Command {
	return commands.BakeCmd()
}
