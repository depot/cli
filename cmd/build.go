package cmd

import (
	"os"
	"os/exec"

	"github.com/depot/cli/pkg/jump"
	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:                "build",
	Short:              "run a Docker build on Depot",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		err := jump.EnsureJump("ckz0c6tu306811i9ko3x7c9id")
		if err != nil {
			return err
		}

		args = append([]string{"buildx", "build", "--builder", "depot-project", "--load"}, args...)
		c := exec.Command("docker", args...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr

		if err := c.Run(); err != nil {
			return err
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(buildCmd)
}
