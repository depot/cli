// Creates a depot project.
package projects

import (
	"encoding/json"
	"fmt"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

func NewCmdCreate() *cobra.Command {
	var (
		token               string
		orgID               string
		region              string
		keepGigabytes       int64
		cachePolicyKeepDays int32
		maxConcurrentBuilds int32
	)

	cmd := &cobra.Command{
		Use:     "create [flags] <project-name>",
		Aliases: []string{"c"},
		Args:    cobra.MaximumNArgs(1),
		Short:   "Create depot project",
		Long: `Create a Depot project with optional cache and autoscaling settings.

Autoscaling adds builder instances when the configured concurrent builds per
instance is reached. Leave the setting unset to disable project autoscaling.`,
		Example: `  # Create a project with autoscaling disabled
  depot projects create my-project

  # Allow up to three concurrent builds per builder instance
  depot projects create my-project --max-concurrent-builds 3`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("project name is required")
			}
			ctx := cmd.Context()
			projectName := args[0]

			if cachePolicyKeepDays < 0 {
				return fmt.Errorf("--cache-policy-keep-days cannot be negative")
			}
			concurrencyChanged := cmd.Flags().Changed("max-concurrent-builds")
			if concurrencyChanged && maxConcurrentBuilds < 1 {
				return fmt.Errorf("--max-concurrent-builds must be greater than zero")
			}

			token, err := helpers.ResolveProjectAuth(ctx, token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			projectClient := api.NewSDKProjectsClient()
			req := corev1.CreateProjectRequest{
				Name:     projectName,
				RegionId: region,
				CachePolicy: &corev1.CachePolicy{
					KeepBytes: proto.Int64(keepGigabytes * 1024 * 1024 * 1024),
					KeepDays:  cachePolicyKeepDays,
				},
			}
			if orgID != "" {
				req.OrganizationId = &orgID
			}
			if concurrencyChanged {
				req.MaxConcurrentBuilds = &maxConcurrentBuilds
			}

			res, err := projectClient.CreateProject(ctx, api.WithAuthentication(connect.NewRequest(&req), token))
			if err != nil {
				return err
			}
			project := NewCreateResponse(res.Msg.GetProject())
			buf, err := json.Marshal(project)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", buf)

			return nil
		},
	}

	flags := cmd.Flags()
	flags.SortFlags = false
	flags.StringVar(&token, "token", "", "Depot token")
	flags.StringVarP(&orgID, "organization", "o", config.GetCurrentOrganization(), "Depot organization ID")
	flags.StringVar(&region, "region", "us-east-1", "Build data will be stored in the chosen region")
	flags.Int64Var(&keepGigabytes, "cache-storage-policy", 50, "Build cache to keep per architecture in GB")
	flags.Int32Var(&cachePolicyKeepDays, "cache-policy-keep-days", 14, "Build cache retention in days (0 means no limit)")
	flags.Int32Var(&maxConcurrentBuilds, "max-concurrent-builds", 0, "Maximum concurrent builds per builder instance before autoscaling (positive integer)")

	return cmd
}

type CreateResponse struct {
	ProjectId           string              `json:"project_id,omitempty"`
	Name                string              `json:"name,omitempty"`
	CachePolicy         *corev1.CachePolicy `json:"cache_policy,omitempty"`
	MaxConcurrentBuilds *int32              `json:"max_concurrent_builds"`
}

func NewCreateResponse(project *corev1.Project) *CreateResponse {
	response := &CreateResponse{
		ProjectId:   project.GetProjectId(),
		Name:        project.GetName(),
		CachePolicy: project.GetCachePolicy(),
	}
	if project != nil {
		response.MaxConcurrentBuilds = project.MaxConcurrentBuilds
	}
	return response
}
