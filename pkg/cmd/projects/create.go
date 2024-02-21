// Creates a depot project.
package projects

import (
	"encoding/json"
	"fmt"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdCreate() *cobra.Command {
	var (
		token         string
		orgID         string
		region        string
		keepGigabytes int64
	)

	cmd := &cobra.Command{
		Use:     "create [flags] <project-name>",
		Aliases: []string{"c"},
		Args:    cobra.MaximumNArgs(1),
		Short:   "Create depot project",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("project name is required")
			}
			ctx := cmd.Context()
			projectName := args[0]

			token, err := helpers.ResolveToken(ctx, token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			projectClient := api.NewSDKProjectsClient()
			req := corev1.CreateProjectRequest{
				Name:           projectName,
				OrganizationId: orgID,
				RegionId:       region,
				CachePolicy: &corev1.CachePolicy{
					KeepBytes: keepGigabytes * 1024 * 1024 * 1024,
				},
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
			fmt.Printf("%s\n", buf)

			return nil
		},
	}

	flags := cmd.Flags()
	flags.SortFlags = false
	flags.StringVar(&token, "token", "", "Depot token")
	flags.StringVarP(&orgID, "organization", "o", "", "Depot organization ID")
	flags.StringVar(&region, "region", "us-east-1", "Build data will be stored in the chosen region")
	flags.Int64Var(&keepGigabytes, "cache-storage-policy", 50, "Build cache to keep per architecture in GB")

	return cmd
}

type CreateResponse struct {
	ProjectId   string              `json:"project_id,omitempty"`
	Name        string              `json:"name,omitempty"`
	CachePolicy *corev1.CachePolicy `json:"cache_policy,omitempty"`
}

func NewCreateResponse(project *corev1.Project) *CreateResponse {
	return &CreateResponse{
		ProjectId:   project.GetProjectId(),
		Name:        project.GetName(),
		CachePolicy: project.GetCachePolicy(),
	}
}
