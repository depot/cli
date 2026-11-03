package projects

import (
	"fmt"
	"math"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func NewCmdUpdate() *cobra.Command {
	var (
		projectID           string
		token               string
		output              string
		name                string
		region              string
		keepGigabytes       int64
		maxConcurrentBuilds int32
		disableAutoscaling  bool
	)

	cmd := &cobra.Command{
		Use:   "update [<project-id>]",
		Short: "Update project details",
		Long: `Update the name, region, cache storage policy, or autoscaling of a Depot project.

Only values passed as flags are changed. If no project ID is provided, the
command uses DEPOT_PROJECT_ID or the project configured in depot.json in the
current directory.

Autoscaling adds builder instances when the configured concurrent builds per
instance is reached. Set a positive limit to enable autoscaling, or disable it
explicitly. Omitting both flags preserves the current setting.

Requires an organization API token. Create one in your organization's
Organization Settings > API Tokens and pass it with --token or DEPOT_TOKEN.`,
		Example: `  # Rename the project configured in the current directory
  depot projects update --name "New project name"

  # Update a specific project's region and print the result as JSON
  depot projects update <project-id> --region eu-central-1 --output json

  # Allow up to three concurrent builds per builder instance
  depot projects update <project-id> --max-concurrent-builds 3

  # Disable project autoscaling
  depot projects update <project-id> --disable-autoscaling`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateProjectOutput(output); err != nil {
				return err
			}

			flags := cmd.Flags()
			nameChanged := flags.Changed("name")
			regionChanged := flags.Changed("region")
			cachePolicyChanged := flags.Changed("cache-storage-policy")
			concurrencyChanged := flags.Changed("max-concurrent-builds")
			if !nameChanged && !regionChanged && !cachePolicyChanged && !concurrencyChanged && !disableAutoscaling {
				return fmt.Errorf("one of --name, --region, --cache-storage-policy, --max-concurrent-builds, or --disable-autoscaling is required")
			}
			if concurrencyChanged && maxConcurrentBuilds < 1 {
				return fmt.Errorf("--max-concurrent-builds must be greater than zero")
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

			token := helpers.ResolveStaticOrgAuth(token)
			if token == "" {
				return fmt.Errorf("missing organization API token; create one in Organization Settings > API Tokens and pass it with --token or DEPOT_TOKEN")
			}

			client := api.NewSDKProjectsClient()
			requestBody := &corev1.UpdateProjectRequest{ProjectId: resolvedProjectID}
			if nameChanged {
				requestBody.Name = &name
			}
			if regionChanged {
				requestBody.RegionId = &region
			}
			if concurrencyChanged {
				requestBody.MaxConcurrentBuilds = &corev1.NullableInt32{
					Kind: &corev1.NullableInt32_Value{Value: maxConcurrentBuilds},
				}
			} else if disableAutoscaling {
				requestBody.MaxConcurrentBuilds = &corev1.NullableInt32{
					Kind: &corev1.NullableInt32_Null{Null: structpb.NullValue_NULL_VALUE},
				}
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
					KeepBytes: proto.Int64(keepGigabytes * bytesPerGigabyte),
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
	cmd.Flags().Int32Var(&maxConcurrentBuilds, "max-concurrent-builds", 0, "Maximum concurrent builds per builder instance before autoscaling (positive integer)")
	cmd.Flags().BoolVar(&disableAutoscaling, "disable-autoscaling", false, "Disable project autoscaling")
	cmd.Flags().StringVar(&token, "token", "", "Depot organization API token (or DEPOT_TOKEN)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	cmd.MarkFlagsMutuallyExclusive("max-concurrent-builds", "disable-autoscaling")

	return cmd
}
