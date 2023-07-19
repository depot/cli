package root

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/depot/cli/pkg/buildx/commands"
	bakeCmd "github.com/depot/cli/pkg/cmd/bake"
	buildCmd "github.com/depot/cli/pkg/cmd/build"
	cacheCmd "github.com/depot/cli/pkg/cmd/cache"
	dockerCmd "github.com/depot/cli/pkg/cmd/docker"
	initCmd "github.com/depot/cli/pkg/cmd/init"
	"github.com/depot/cli/pkg/cmd/list"
	loginCmd "github.com/depot/cli/pkg/cmd/login"
	logout "github.com/depot/cli/pkg/cmd/logout"
	versionCmd "github.com/depot/cli/pkg/cmd/version"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/docker"
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

	dockerCli, err := docker.NewDockerCLI()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Child commands
	cmd.AddCommand(bakeCmd.NewCmdBake(dockerCli))
	cmd.AddCommand(buildCmd.NewCmdBuild(dockerCli))
	cmd.AddCommand(cacheCmd.NewCmdCache())
	cmd.AddCommand(initCmd.NewCmdInit())
	cmd.AddCommand(list.NewCmdList())
	cmd.AddCommand(loginCmd.NewCmdLogin())
	cmd.AddCommand(logout.NewCmdLogout())
	cmd.AddCommand(versionCmd.NewCmdVersion(version, buildDate))
	cmd.AddCommand(dockerCmd.NewCmdConfigureDocker())
	cmd.AddCommand(commands.NewBuildxCmd(dockerCli))

	return cmd
}
