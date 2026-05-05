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

func newSandboxLogs() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <sandbox-id>",
		Short: "Stream entrypoint stdout/stderr from a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sandboxID := args[0]

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

			// Modal-only guard: StreamSandboxLogs is wired to the agent
			// modal log channel, so it returns nothing for vanilla / compose-
			// wrapped sandboxes (spec.AgentType unset). Bail out with a
			// pointer at the streams that *do* carry their output.
			info, err := client.GetSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(&agentv1.GetSandboxRequest{SandboxId: sandboxID}), token, orgID))
			if err != nil {
				return fmt.Errorf("get sandbox: %w", err)
			}
			if sb := info.Msg.Sandbox; sb == nil || sb.GetAgentType() == agentv1.AgentType_AGENT_TYPE_UNSPECIFIED {
				return fmt.Errorf("sandbox %s has no agent_type — modal log stream is empty for vanilla / compose-wrapped sandboxes.\n  Axiom: ['vm-execution-log'] | where sandbox_id == \"%s\"\n  Shell: depot sandbox shell %s\n", sandboxID, sandboxID, sandboxID)
			}

			return streamLogs(ctx, client, token, orgID, sandboxID, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	// Reserved for future support; the server stream closes naturally when the
	// sandbox terminates, so a follow flag isn't required for parity with `tail -f`.
	cmd.Flags().BoolP("follow", "f", true, "Follow the log stream until the sandbox exits")
	return cmd
}
