package docker

import (
	"fmt"

	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/dockerclient"
	"github.com/depot/cli/pkg/helpers"
	configtypes "github.com/docker/cli/cli/config/types"
	registrytypes "github.com/docker/docker/api/types/registry"
	"github.com/spf13/cobra"
)

func NewCmdLoginDocker() *cobra.Command {
	var (
		org   string
		token string
	)

	cmd := &cobra.Command{
		Use:   "login-docker",
		Short: "Log in Docker to the Depot registry with your user API token",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := helpers.ResolveOrgAuth(cmd.Context(), token)
			if err != nil {
				return err
			}
			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			if org == "" {
				org = config.GetCurrentOrganization()
			}
			if org == "" {
				return fmt.Errorf("No organization configured; use the --org flag or verify your project configuration")
			}

			dockerCli, err := dockerclient.NewDockerCLI()
			if err != nil {
				return err
			}

			serverAddress := org + ".registry.depot.dev"
			client := dockerCli.Client()
			_, err = client.RegistryLogin(cmd.Context(), registrytypes.AuthConfig{
				Username:      "x-token",
				Password:      token,
				ServerAddress: serverAddress,
			})
			if err != nil {
				return err
			}

			cfg := dockerCli.ConfigFile()
			creds := cfg.GetCredentialsStore(serverAddress)
			err = creds.Store(configtypes.AuthConfig{
				Username:      "x-token",
				Password:      token,
				ServerAddress: serverAddress,
			})
			if err != nil {
				return err
			}

			fmt.Println("Successfully stored Docker credentials for " + serverAddress)
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&org, "org", "", "Depot organization ID")
	flags.StringVar(&token, "token", "", "Depot token")

	return cmd
}
