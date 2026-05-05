package sandbox

import (
	"fmt"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
)

func newSandboxList() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List sandboxes for the current organization",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			token, _ := cmd.Flags().GetString("token")
			token, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return fmt.Errorf("resolve token: %w", err)
			}
			orgID, _ := cmd.Flags().GetString("org")
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			client := api.NewSandboxClient()
			res, err := client.ListSandboxs(ctx, api.WithAuthenticationAndOrg(
				connect.NewRequest(&agentv1.ListSandboxsRequest{}), token, orgID))
			if err != nil {
				return fmt.Errorf("list sandboxes: %w", err)
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SANDBOX ID\tSESSION ID\tCREATED\tSTATUS\tEXIT")
			for _, s := range res.Msg.Sandboxes {
				status := "running"
				if s.CompletedAt != nil {
					status = "completed"
				}
				exit := "-"
				if s.ExitCode != nil {
					exit = fmt.Sprintf("%d", *s.ExitCode)
				}
				created := "-"
				if s.CreatedAt != nil {
					created = s.CreatedAt.AsTime().Format("2006-01-02 15:04")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.SandboxId, s.SessionId, created, status, exit)
			}
			return tw.Flush()
		},
	}
	return cmd
}
