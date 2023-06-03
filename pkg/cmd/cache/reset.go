package init

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bufbuild/connect-go"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/docker/cli/cli"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func getToken() (string, error) {
	token := os.Getenv("DEPOT_TOKEN")
	if token == "" {
		token = config.GetApiToken()
	}
	if token == "" {
		return "", fmt.Errorf("missing API token, please run `depot login`")
	}
	return token, nil
}

func NewCmdResetCache() *cobra.Command {
	var projectID string
	var token string

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset the cache for a project",
		Args:  cli.RequiresMaxArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := filepath.Abs(args[0])
			if err != nil {
				return fmt.Errorf("unable to determine absolute path: %w", err)
			}

			projectID := helpers.ResolveProjectID(projectID, cwd)
			if projectID == "" {
				return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
			}
			if projectID == "" {
				return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
			}

			// TODO: make this a helper
			token, err := getToken()
			if err != nil {
				return err
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
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")

	return cmd
}
