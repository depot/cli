package build

import (
	"os"
	"os/exec"

	"github.com/depot/cli/pkg/jump"
	"github.com/spf13/cobra"
)

func NewCmdBuild() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "build",
		Short:              "run a Docker build on Depot",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := os.Getenv("DEPOT_PROJECT_ID")
			if projectID == "" {
				projectID = "ckzvubrmp05214iu5u6q7lnrw"
			}

			err := jump.EnsureJump(projectID)
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

	return cmd
}
