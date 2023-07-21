package version

import (
	"fmt"
	"os"
	"os/exec"
	"path"

	"github.com/docker/cli/cli/config"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdConfigureDocker() *cobra.Command {
	shimBuildx := false

	cmd := &cobra.Command{
		Use:   "configure-docker",
		Short: "Configure Docker to use Depot for builds",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := config.Dir()
			if err := os.MkdirAll(dir, 0755); err != nil {
				return errors.Wrap(err, "could not create docker config")
			}

			self, err := os.Executable()
			if err != nil {
				return errors.Wrap(err, "could not find executable")
			}

			if err := installDepotPlugin(dir, self); err != nil {
				return errors.Wrap(err, "could not install depot plugin")
			}

			if shimBuildx {
				if err := installDepotBuildxPlugin(dir, self); err != nil {
					return errors.Wrap(err, "could not install depot plugin")
				}
			}

			if err := useDepotBuilderAlias(dir); err != nil {
				return errors.Wrap(err, "could not set depot builder alias")
			}

			fmt.Println("Successfully installed Depot as a Docker CLI plugin")

			return nil
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&shimBuildx, "shim-buildx", false, "Shim docker buildx build to use Depot")

	return cmd
}

func installDepotPlugin(dir, self string) error {
	if err := os.MkdirAll(path.Join(config.Dir(), "cli-plugins"), 0755); err != nil {
		return errors.Wrap(err, "could not create cli-plugins directory")
	}

	symlink := path.Join(config.Dir(), "cli-plugins", "docker-depot")

	err := os.RemoveAll(symlink)
	if err != nil {
		return errors.Wrap(err, "could not remove existing symlink")
	}

	err = os.Symlink(self, symlink)
	if err != nil {
		return errors.Wrap(err, "could not create symlink")
	}

	return nil
}

func installDepotBuildxPlugin(dir, self string) error {
	if err := os.MkdirAll(path.Join(config.Dir(), "cli-plugins"), 0755); err != nil {
		return errors.Wrap(err, "could not create cli-plugins directory")
	}

	symlink := path.Join(config.Dir(), "cli-plugins", "docker-buildx")
	original := path.Join(config.Dir(), "cli-plugins", "original-docker-buildx")
	global := "/usr/libexec/docker/cli-plugins/docker-buildx"

	// If original plugin symlink does not exist, create it

	if _, err := os.Stat(original); err != nil {
		if !os.IsNotExist(err) {
			return errors.Wrap(err, "could not stat original-docker-buildx plugin")
		}

		if _, err := os.Stat(symlink); err == nil {
			err = os.Rename(symlink, original)
			if err != nil {
				return errors.Wrap(err, "could not rename existing symlink")
			}
		} else {
			candidate, err := exec.LookPath("docker-buildx")
			if err != nil {
				if _, err := os.Stat(global); err == nil {
					candidate = global
				} else {
					return errors.Wrap(err, "could not find docker-buildx plugin")
				}
			}
			err = os.Symlink(candidate, original)
			if err != nil {
				return errors.Wrap(err, "could not create original-docker-buildx plugin")
			}
		}
	}

	// Original plugin exists, update current symlink

	err := os.RemoveAll(symlink)
	if err != nil {
		return errors.Wrap(err, "could not remove existing symlink")
	}

	err = os.Symlink(self, symlink)
	if err != nil {
		return errors.Wrap(err, "could not create symlink")
	}

	return nil
}

func useDepotBuilderAlias(dir string) error {
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

	return nil
}
