package sandbox

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
	"github.com/spf13/cobra"
)

// newSandboxKill wraps depot.sandbox.v1.SandboxService.KillSandbox — the
// forced-termination verb (terminated_by=FORCED). No hooks; if you need
// hooks, use `depot sandbox stop`.
func newSandboxKill() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kill [<sandbox-id>...]",
		Short: "Force-terminate one or more sandboxes",
		Long: `Force-terminate sandboxes by id via depot.sandbox.v1.KillSandbox.

For a graceful shutdown that fires on.down hooks first, use 'depot sandbox stop'.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			token, orgID, err := resolveAuthAndOrg(ctx, cmd)
			if err != nil {
				return err
			}

			ids := args

			client := api.NewSandboxV0Client()
			var failures []string
			for _, id := range ids {
				req := &sandboxv1.KillSandboxRequest{Sandbox: sandboxRef(id)}
				_, err := client.KillSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
				if err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", id, err))
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "killed %s\n", id)
			}
			if len(failures) > 0 {
				return fmt.Errorf("kill failed:\n  %s", strings.Join(failures, "\n  "))
			}
			return nil
		},
	}
	return cmd
}
