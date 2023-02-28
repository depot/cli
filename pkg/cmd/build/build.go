package build

import (
	"github.com/depot/cli/pkg/buildx/commands"
	_ "github.com/depot/cli/pkg/buildxdriver"
	"github.com/docker/cli/cli/command"
	"github.com/spf13/cobra"
)

func NewCmdBuild(dockerCli command.Cli) *cobra.Command {
	return commands.BuildCmd(dockerCli)
}
