package init

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/bufbuild/connect-go"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/docker/cli/cli"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdResetCache() *cobra.Command {
	var projectID string
	var token string

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset the cache for a project",
		Args:  cli.RequiresMaxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var cwd string
			if len(args) > 0 {
				cwd, _ = filepath.Abs(args[0])
			}
			projectID := helpers.ResolveProjectID(projectID, cwd)
			if projectID == "" {
				return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
			}

			var err error
			token, err = helpers.ResolveToken(context.Background(), token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			client := api.NewProjectsClient()
			req := cliv1beta1.ResetProjectCacheRequest{ProjectId: projectID}
			resp, err := client.ResetProjectCache(context.TODO(), api.WithAuthentication(connect.NewRequest(&req), token))
			if err != nil {
				return err
			}

			fmt.Printf("Cache reset for %s (%s)\n", resp.Msg.Name, resp.Msg.OrgName)

			return nil
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Depot project ID for the cache to reset")
	cmd.Flags().StringVar(&token, "token", "", "Depot token")

	return cmd
}
