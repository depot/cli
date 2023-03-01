package init

import (
	"github.com/depot/cli/pkg/buildx/commands"
	"github.com/docker/cli/cli/command"
	"github.com/spf13/cobra"
)

func NewCmdBake(dockerCli command.Cli) *cobra.Command {
	return commands.BakeCmd(dockerCli)
}
