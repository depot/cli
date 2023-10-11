package init

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/project"
	"github.com/docker/cli/cli"
	"github.com/spf13/cobra"
)

func NewCmdInit() *cobra.Command {
	var (
		projectID string
		token     string
	)

	cmd := &cobra.Command{
		Use:   "init [flags] [<dir>]",
		Short: "Create a `depot.json` project config",
		Args:  cli.RequiresMaxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			contextDir := "."
			if len(args) > 0 {
				contextDir = args[0]
			}

			absContext, err := filepath.Abs(contextDir)
			if err != nil {
				return err
			}

			_, existingFile, _ := project.ReadConfig(absContext)
			if existingFile != "" && !force {
				return fmt.Errorf("Project configuration %s already exists at path \"%s\", re-run with `--force` to overwrite", filepath.Base(existingFile), filepath.Dir(existingFile))
			}

			token, err = helpers.ResolveToken(context.Background(), token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			selectedProject, err := helpers.InitializeProject(context.Background(), token, projectID)
			if err != nil {
				return err
			}

			configFilepath := existingFile
			if configFilepath == "" {
				configFilepath = filepath.Join(absContext, "depot.json")
			}

			err = selectedProject.SaveAs(configFilepath)
			if err != nil {
				return err
			}

			return nil
		},
	}

	cmd.Flags().Bool("force", false, "Overwrite any existing project configuration")
	cmd.Flags().StringVar(&projectID, "project", "", "The ID of the project to initialize")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")

	return cmd
}
