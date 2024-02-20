// Creates a depot project.
package projects

import (
	"encoding/json"
	"fmt"
	"time"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdCreate() *cobra.Command {
	var (
		token         string
		region        string
		keepGigabytes int64
	)

	cmd := &cobra.Command{
		Use:     "create [flags] <project-name>",
		Aliases: []string{"c"},
		Args:    cobra.ExactArgs(1),
		Hidden:  true,
		Short:   "Create depot project",
		RunE: func(cmd *cobra.Command, args []string) error {
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
				Name:     projectName,
				RegionId: region,
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
	flags.StringVar(&token, "token", "", "Depot token")
	flags.StringVar(&region, "region", "us-east-1", "Build data will be stored in the chosen region")
	flags.Int64Var(&keepGigabytes, "keep-cache-size", 50, "Build cache to keep per architecture in GB")

	return cmd
}

type CreateResponse struct {
	ProjectId      string              `json:"project_id,omitempty"`
	OrganizationId string              `json:"organization_id,omitempty"`
	Name           string              `json:"name,omitempty"`
	RegionId       string              `json:"region_id,omitempty"`
	CreatedAt      time.Time           `json:"created_at,omitempty"`
	CachePolicy    *corev1.CachePolicy `json:"cache_policy,omitempty"`
}

func NewCreateResponse(project *corev1.Project) *CreateResponse {
	var createdAt time.Time
	if project.GetCreatedAt() != nil {
		createdAt = project.GetCreatedAt().AsTime()
	}
	return &CreateResponse{
		ProjectId:      project.GetProjectId(),
		OrganizationId: project.GetOrganizationId(),
		Name:           project.GetName(),
		RegionId:       project.GetRegionId(),
		CreatedAt:      createdAt,
		CachePolicy:    project.GetCachePolicy(),
	}
}
