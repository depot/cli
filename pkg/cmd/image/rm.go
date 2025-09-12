package image

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	v1 "github.com/depot/cli/pkg/proto/depot/build/v1"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdRM() *cobra.Command {
	var token string
	var force bool
	var digests []string

	cmd := &cobra.Command{
		Use:     "rm PROJECT --digest DIGEST [--digest DIGEST...]",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove images from the registry by digest",
		Long: `Remove images from the registry by digest.

To find image digests, use 'depot image list --project PROJECT'.
Images are referenced by their sha256 digest.`,
		Example: `  # Delete a single image by digest
  depot image rm myproject --digest sha256:abc123...
  
  # Delete multiple images
  depot image rm myproject --digest sha256:abc123... --digest sha256:def456...
  
  # Force deletion without confirmation
  depot image rm myproject --digest sha256:abc123... --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			projectID := args[0]

			if len(digests) == 0 {
				return errors.New("at least one --digest is required")
			}

			// Convert digests to the format ECR expects
			var imageTags []string
			for _, digest := range digests {
				// The ECR API expects image tags in the format "sha256-<digest>" rather than the standard "sha256:<digest>".
				// This transformation ensures compatibility with the ECR API by converting the prefix.
				digest = strings.TrimPrefix(digest, "sha256:")
				imageTags = append(imageTags, "sha256-"+digest)
			}

			token, err := helpers.ResolveProjectAuth(context.Background(), token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			totalImages := len(digests)
			if !force {
				fmt.Printf("Are you sure you want to delete %d image(s)? [y/N]: ", totalImages)
				var response string
				if _, err := fmt.Scanln(&response); err != nil {
					return fmt.Errorf("error reading input: %w", err)
				}
				if response != "y" && response != "Y" {
					fmt.Println("Operation cancelled")
					return nil
				}
			}

			client := api.NewRegistryClient()

			req := connect.NewRequest(&v1.DeleteImageRequest{
				ProjectId: projectID,
				ImageTags: imageTags,
			})

			req = api.WithAuthentication(req, token)
			_, err = client.DeleteImage(ctx, req)
			if err != nil {
				return fmt.Errorf("failed to delete images: %v", err)
			}

			if totalImages == 1 {
				fmt.Printf("Successfully deleted image with digest: %s\n", digests[0])
			} else {
				fmt.Printf("Successfully deleted %d images\n", totalImages)
			}

			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringSliceVar(&digests, "digest", []string{}, "Image digest(s) to delete (can be specified multiple times)")
	flags.StringVar(&token, "token", "", "Depot token")
	flags.BoolVarP(&force, "force", "f", false, "Force deletion without confirmation")

	cmd.MarkFlagRequired("digest")

	return cmd
}
