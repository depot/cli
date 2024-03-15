package pulltoken

import (
	"fmt"

	"connectrpc.com/connect"
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	"github.com/spf13/cobra"
)

func NewCmdPullToken(dockerCli command.Cli) *cobra.Command {
	var (
		token     string
		projectID string
		buildID   string
	)

	cmd := &cobra.Command{
		Use:   "pull-token [flags] ([buildID])",
		Short: "Create a new pull token for the ephemeral registry",
		Args:  cli.RequiresMaxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				buildID = args[0]
			}

			ctx := cmd.Context()

			token, err := helpers.ResolveToken(ctx, token)
			if err != nil {
				return err
			}
			projectID = helpers.ResolveProjectID(projectID)

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			client := depotapi.NewBuildClient()
			req := &cliv1.GetPullTokenRequest{BuildId: &buildID, ProjectId: &projectID}
			res, err := client.GetPullToken(ctx, depotapi.WithAuthentication(connect.NewRequest(req), token))
			if err != nil {
				return err
			}

			fmt.Println(res.Msg.Token)
			return nil
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Depot project ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot token")

	return cmd
}
