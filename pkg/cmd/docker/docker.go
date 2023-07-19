package version

import (
	"fmt"
	"os"
	"path"

	"github.com/docker/cli/cli/config"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdDocker() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "docker",
		Hidden: true,
	}

	cmd.AddCommand(NewCmdDockerInstall())

	return cmd
}

func NewCmdDockerInstall() *cobra.Command {
	cmd := &cobra.Command{
		Use: "install",
		RunE: func(cmd *cobra.Command, args []string) error {

			dir := config.Dir()
			if err := os.MkdirAll(dir, 0755); err != nil {
				return errors.Wrap(err, "could not create docker config")
			}

			if err := os.MkdirAll(path.Join(config.Dir(), "cli-plugins"), 0755); err != nil {
				return errors.Wrap(err, "could not create cli-plugins directory")
			}

			self, err := os.Executable()
			if err != nil {
				return errors.Wrap(err, "could not find executable")
			}

			symlink := path.Join(config.Dir(), "cli-plugins", "docker-depot")

			err = os.RemoveAll(symlink)
			if err != nil {
				return errors.Wrap(err, "could not remove existing symlink")
			}

			err = os.Symlink(self, symlink)
			if err != nil {
				return errors.Wrap(err, "could not create symlink")
			}

			cfg, err := config.Load(dir)
			if err != nil {
				return err
			}

			if cfg.Aliases == nil {
				cfg.Aliases = map[string]string{}
			}
			cfg.Aliases["builder"] = "depot"

			if err := cfg.Save(); err != nil {
				return errors.Wrap(err, "could not write docker config")
			}

			fmt.Println("Successfully installed Depot as a Docker CLI plugin")

			return nil
		},
	}
	return cmd
}
