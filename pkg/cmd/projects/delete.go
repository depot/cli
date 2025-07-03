package projects

import (
	"fmt"

	v1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/charmbracelet/huh"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdDelete() *cobra.Command {
	var (
		token     string
		orgID     string
		projectID string
		confirm   bool
	)

	cmd := &cobra.Command{
		Use:     "delete",
		Aliases: []string{"d"},
		Args:    cobra.MaximumNArgs(1),
		Short:   "Delete a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Resolve token first
			token, err := helpers.ResolveToken(cmd.Context(), token)
			if err != nil {
				return err
			}

			if projectID == "" {
				// List projects
				projects, err := helpers.RetrieveProjects(ctx, token)
				if err != nil {
					return err
				}

				// Select project to delete
				projectID, err = helpers.SelectProject(projects.Projects)
				if err != nil {
					return err
				}
			}

			// If not confirmed through flag already, ask for confirmation interactively
			if !confirm {
				err := huh.NewConfirm().
					Title(fmt.Sprintf("Are you sure you want to delete project %s?", projectID)).
					Affirmative("Yes!").
					Negative("No.").
					Value(&confirm).
					Run()
				if err != nil {
					return err
				}
			}

			// If not confirmed, exit
			if !confirm {
				fmt.Println("Cancelling project deletion...")
				return nil
			}

			// Delete project
			client := api.NewSDKProjectsClient()
			req := v1.DeleteProjectRequest{
				ProjectId: projectID,
			}

			_, err = client.DeleteProject(ctx, api.WithAuthentication(connect.NewRequest(&req), token))
			if err != nil {
				return err
			}

			fmt.Printf("Successfully deleted project %s\n", projectID)
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&token, "token", "t", "", "Depot API token")
	flags.StringVarP(&orgID, "organization", "o", config.GetCurrentOrganization(), "Depot organization ID")
	flags.StringVarP(&projectID, "project-id", "p", "", "The ID of the project to delete")
	flags.BoolVarP(&confirm, "yes", "y", false, "Confirm deletion")

	return cmd
}
