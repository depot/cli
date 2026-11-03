package list

import (
	"fmt"

	"github.com/depot/cli/pkg/cmd/jobs"
	"github.com/spf13/cobra"
)

func NewCmdList() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Display Depot project, build, and GitHub Actions job information",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("missing subcommand, please run `depot list --help`")
		},
	}

	cmd.AddCommand(NewCmdProjects("projects", "p"))
	cmd.AddCommand(NewCmdBuilds())
	cmd.AddCommand(jobs.NewCmdListJobs())

	return cmd
}
