package root

import (
	"github.com/spf13/cobra"

	buildCmd "github.com/depot/cli/pkg/cmd/build"
	debugCmd "github.com/depot/cli/pkg/cmd/debug"
	dialstdioCmd "github.com/depot/cli/pkg/cmd/dialstdio"
	jumpCmd "github.com/depot/cli/pkg/cmd/jump"
	loginCmd "github.com/depot/cli/pkg/cmd/login"
	versionCmd "github.com/depot/cli/pkg/cmd/version"
)

func NewCmdRoot(version string) *cobra.Command {
	var cmd = &cobra.Command{
		Use:           "depot <command> [flags]",
		Short:         "Depot CLI",
		SilenceErrors: true,
		// SilenceUsage:  true,

		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Usage()
		},
	}

	formattedVersion := versionCmd.Format(version)
	cmd.SetVersionTemplate(formattedVersion)
	cmd.Version = formattedVersion
	cmd.Flags().Bool("version", false, "Print the version and exit")

	// Child commands
	cmd.AddCommand(buildCmd.NewCmdBuild())
	cmd.AddCommand(debugCmd.NewCmdDebug())
	cmd.AddCommand(dialstdioCmd.NewCmdDialStdio())
	cmd.AddCommand(jumpCmd.NewCmdJump())
	cmd.AddCommand(loginCmd.NewCmdLogin())
	cmd.AddCommand(versionCmd.NewCmdVersion(version))

	return cmd
}
