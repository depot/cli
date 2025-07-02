package root

import (
	"os"

	"github.com/spf13/cobra"

	bakeCmd "github.com/depot/cli/pkg/cmd/bake"
	buildCmd "github.com/depot/cli/pkg/cmd/build"
	cacheCmd "github.com/depot/cli/pkg/cmd/cache"
	cargoCmd "github.com/depot/cli/pkg/cmd/cargo"
	claudeCmd "github.com/depot/cli/pkg/cmd/claude"
	dockerCmd "github.com/depot/cli/pkg/cmd/docker"
	"github.com/depot/cli/pkg/cmd/exec"
	"github.com/depot/cli/pkg/cmd/gocache"
	initCmd "github.com/depot/cli/pkg/cmd/init"
	"github.com/depot/cli/pkg/cmd/list"
	loginCmd "github.com/depot/cli/pkg/cmd/login"
	logout "github.com/depot/cli/pkg/cmd/logout"
	"github.com/depot/cli/pkg/cmd/org"
	"github.com/depot/cli/pkg/cmd/projects"
	"github.com/depot/cli/pkg/cmd/pull"
	"github.com/depot/cli/pkg/cmd/pulltoken"
	"github.com/depot/cli/pkg/cmd/push"
	"github.com/depot/cli/pkg/cmd/registry"
	versionCmd "github.com/depot/cli/pkg/cmd/version"
	"github.com/depot/cli/pkg/config"
)

func NewCmdRoot(version, buildDate string) *cobra.Command {
	var dockerConfig string

	var cmd = &cobra.Command{
		Use:          "depot <command> [flags]",
		Short:        "Depot CLI",
		SilenceUsage: true,

		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Usage()
		},

		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if dockerConfig != "" {
				os.Setenv("DOCKER_CONFIG", dockerConfig)
			}
		},
	}

	// Initialize config
	_ = config.NewConfig()

	formattedVersion := versionCmd.Format(version, buildDate)
	cmd.SetVersionTemplate(formattedVersion)
	cmd.Version = formattedVersion
	cmd.Flags().Bool("version", false, "Print the version and exit")

	cmd.PersistentFlags().StringVar(&dockerConfig, "config", "", "Override the location of Docker client config files")
	_ = cmd.PersistentFlags().MarkHidden("config")

	// Child commands
	cmd.AddCommand(bakeCmd.NewCmdBake())
	cmd.AddCommand(buildCmd.NewCmdBuild())
	cmd.AddCommand(cacheCmd.NewCmdCache())
	cmd.AddCommand(cargoCmd.NewCmdCargo())
	cmd.AddCommand(claudeCmd.NewCmdClaude())
	cmd.AddCommand(initCmd.NewCmdInit())
	cmd.AddCommand(list.NewCmdList())
	cmd.AddCommand(loginCmd.NewCmdLogin())
	cmd.AddCommand(logout.NewCmdLogout())
	cmd.AddCommand(pull.NewCmdPull())
	cmd.AddCommand(pulltoken.NewCmdPullToken())
	cmd.AddCommand(push.NewCmdPush())
	cmd.AddCommand(versionCmd.NewCmdVersion(version, buildDate))
	cmd.AddCommand(dockerCmd.NewCmdConfigureDocker())
	cmd.AddCommand(registry.NewCmdRegistry())
	cmd.AddCommand(org.NewCmdOrg())
	cmd.AddCommand(projects.NewCmdProjects())
	cmd.AddCommand(gocache.NewCmdGoCache())
	cmd.AddCommand(exec.NewCmdExec())

	return cmd
}
