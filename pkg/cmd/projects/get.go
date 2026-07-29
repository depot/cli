package projects

import (
	"fmt"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdGet() *cobra.Command {
	var (
		projectID string
		token     string
		output    string
	)

	cmd := &cobra.Command{
		Use:   "get [<project-id>]",
		Short: "Get project details",
		Long: `Get the details and configuration of a Depot project.

If no project ID is provided, the command uses DEPOT_PROJECT_ID or the project
configured in depot.json in the current directory.`,
		Example: `  # Get the project configured in the current directory
  depot projects get

  # Get a specific project as JSON
  depot projects get <project-id> --output json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateProjectOutput(output); err != nil {
				return err
			}

			resolvedProjectID, err := resolveProjectID(args, projectID)
			if err != nil {
				return err
			}

			token, err := helpers.ResolveProjectAuth(cmd.Context(), token)
			if err != nil {
				return err
			}
			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			request := connect.NewRequest(&corev1.GetProjectRequest{ProjectId: resolvedProjectID})
			response, err := api.NewSDKProjectsClient().GetProject(
				cmd.Context(),
				api.WithAuthentication(request, token),
			)
			if err != nil {
				return fmt.Errorf("failed to get project: %w", err)
			}

			return writeProjectOutput(cmd.OutOrStdout(), response.Msg.GetProject(), output)
		},
	}

	cmd.Flags().StringVarP(&projectID, "project-id", "p", "", "Depot project ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")

	return cmd
}
