package image

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	v1 "github.com/depot/cli/pkg/proto/depot/build/v1"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdRM() *cobra.Command {
	var token string
	var projectID string

	cmd := &cobra.Command{
		Use:     "rm <tag> [<tag>...]",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove images from the registry by tag",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			projectID = helpers.ResolveProjectID(projectID)
			if projectID == "" {
				return errors.New("please specify a project ID")
			}

			imageTags := args

			token, err := helpers.ResolveProjectAuth(ctx, token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			client := api.NewRegistryClient()
			req := connect.NewRequest(&v1.DeleteImageRequest{ProjectId: projectID, ImageTags: imageTags})
			_, err = client.DeleteImage(ctx, api.WithAuthentication(req, token))
			if err != nil {
				return fmt.Errorf("failed to delete images: %v", err)
			}

			totalImages := len(imageTags)
			if totalImages == 1 {
				fmt.Printf("Successfully deleted image with tag: %s\n", imageTags[0])
			} else {
				fmt.Printf("Successfully deleted %d images\n", totalImages)
			}

			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&projectID, "project", "", "Depot project ID")
	flags.StringVar(&token, "token", "", "Depot token")

	return cmd
}
