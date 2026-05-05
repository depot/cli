package sandbox

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
)

func newSandboxKill() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kill <sandbox-id>...",
		Short: "Terminate one or more sandboxes",
		Args:  cobra.MinimumNArgs(1),
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
			var failures []string
			for _, id := range args {
				_, err := client.KillSandbox(ctx, api.WithAuthenticationAndOrg(
					connect.NewRequest(&agentv1.KillSandboxRequest{SandboxId: id}), token, orgID))
				if err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", id, err))
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "killed %s\n", id)
			}
			if len(failures) > 0 {
				return fmt.Errorf("kill failed:\n  %s", failures)
			}
			return nil
		},
	}
	return cmd
}
