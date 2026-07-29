package projects

import (
	"fmt"
	"math"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdUpdate() *cobra.Command {
	var (
		projectID     string
		token         string
		output        string
		name          string
		region        string
		keepGigabytes int64
	)

	cmd := &cobra.Command{
		Use:   "update [<project-id>]",
		Short: "Update project details",
		Long: `Update the name, region, or cache storage policy of a Depot project.

Only values passed as flags are changed. If no project ID is provided, the
command uses DEPOT_PROJECT_ID or the project configured in depot.json in the
current directory.`,
		Example: `  # Rename the project configured in the current directory
  depot projects update --name "New project name"

  # Update a specific project's region and print the result as JSON
  depot projects update <project-id> --region eu-central-1 --output json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateProjectOutput(output); err != nil {
				return err
			}

			flags := cmd.Flags()
			nameChanged := flags.Changed("name")
			regionChanged := flags.Changed("region")
			cachePolicyChanged := flags.Changed("cache-storage-policy")
			if !nameChanged && !regionChanged && !cachePolicyChanged {
				return fmt.Errorf("one of --name, --region, or --cache-storage-policy is required")
			}
			if cachePolicyChanged {
				if keepGigabytes < 0 {
					return fmt.Errorf("--cache-storage-policy cannot be negative")
				}
				if keepGigabytes > math.MaxInt64/bytesPerGigabyte {
					return fmt.Errorf("--cache-storage-policy is too large")
				}
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

			client := api.NewSDKProjectsClient()
			requestBody := &corev1.UpdateProjectRequest{ProjectId: resolvedProjectID}
			if nameChanged {
				requestBody.Name = &name
			}
			if regionChanged {
				requestBody.RegionId = &region
			}
			if cachePolicyChanged {
				getRequest := connect.NewRequest(&corev1.GetProjectRequest{ProjectId: resolvedProjectID})
				getResponse, err := client.GetProject(
					cmd.Context(),
					api.WithAuthentication(getRequest, token),
				)
				if err != nil {
					return fmt.Errorf("failed to get current project cache policy: %w", err)
				}
				currentProject := getResponse.Msg.GetProject()
				if currentProject == nil {
					return fmt.Errorf("API returned no project")
				}

				requestBody.CachePolicy = &corev1.CachePolicy{
					KeepBytes: keepGigabytes * bytesPerGigabyte,
					KeepDays:  currentProject.GetCachePolicy().GetKeepDays(),
				}
			}

			request := connect.NewRequest(requestBody)
			response, err := client.UpdateProject(
				cmd.Context(),
				api.WithAuthentication(request, token),
			)
			if err != nil {
				return fmt.Errorf("failed to update project: %w", err)
			}

			return writeProjectOutput(cmd.OutOrStdout(), response.Msg.GetProject(), output)
		},
	}

	cmd.Flags().StringVarP(&projectID, "project-id", "p", "", "Depot project ID")
	cmd.Flags().StringVar(&name, "name", "", "Project name")
	cmd.Flags().StringVar(&region, "region", "", "Build data storage region")
	cmd.Flags().Int64Var(&keepGigabytes, "cache-storage-policy", 0, "Build cache to keep per architecture in GB")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")

	return cmd
}
