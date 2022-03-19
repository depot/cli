package root

import (
	"github.com/spf13/cobra"

	buildCmd "github.com/depot/cli/pkg/cmd/build"
	loginCmd "github.com/depot/cli/pkg/cmd/login"
	versionCmd "github.com/depot/cli/pkg/cmd/version"
	"github.com/depot/cli/pkg/config"
)

func NewCmdRoot(version, buildDate string) *cobra.Command {
	var cmd = &cobra.Command{
		Use:          "depot <command> [flags]",
		Short:        "Depot CLI",
		SilenceUsage: true,

		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Usage()
		},
	}

	// Initialize config
	_ = config.NewConfig()

	formattedVersion := versionCmd.Format(version, buildDate)
	cmd.SetVersionTemplate(formattedVersion)
	cmd.Version = formattedVersion
	cmd.Flags().Bool("version", false, "Print the version and exit")

	// Child commands
	cmd.AddCommand(buildCmd.NewCmdBuild())
	cmd.AddCommand(loginCmd.NewCmdLogin())
	cmd.AddCommand(versionCmd.NewCmdVersion(version, buildDate))

	return cmd
}
