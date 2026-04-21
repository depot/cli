package sandbox

import (
	"fmt"
	"os"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

func newSandboxExec() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec [flags]",
		Short: "Execute a command within the compute",
		Long:  "Execute a command within the compute",
		Example: `
  # execute command within the compute
  depot sandbox exec --sandbox-id 1234567890 --session-id 1234567890 -- /bin/bash -lc whoami

  # execute command with timeout (30 seconds)
  depot sandbox exec --sandbox-id 1234567890 --session-id 1234567890 --timeout 30000 -- /bin/bash -lc whoami

  # execute complex command
  depot sandbox exec --sandbox-id 1234567890 --session-id 1234567890 -- /bin/bash -lc 'for i in {1..10}; do echo $i; sleep 1; done'
`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token, err := cmd.Flags().GetString("token")
			cobra.CheckErr(err)

			token, err = helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return fmt.Errorf("failed to resolve token: %w", err)
			}

			orgID, err := cmd.Flags().GetString("org")
			cobra.CheckErr(err)

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			sandboxID, err := cmd.Flags().GetString("sandbox-id")
			cobra.CheckErr(err)

			if sandboxID == "" {
				return fmt.Errorf("sandbox-id is required")
			}

			sessionID, err := cmd.Flags().GetString("session-id")
			cobra.CheckErr(err)

			if sessionID == "" {
				return fmt.Errorf("session-id is required")
			}

			timeout, err := cmd.Flags().GetInt("timeout")
			cobra.CheckErr(err)

			client := api.NewComputeClient()

			stream, err := client.RemoteExec(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(&civ1.ExecuteCommandRequest{
				SandboxId: sandboxID,
				SessionId: sessionID,
				Command: &civ1.Command{
					CommandArray: args,
					TimeoutMs:    int32(timeout),
				},
			}), token, orgID))
			if err != nil {
				// nolint:wrapcheck
				return err
			}

			for stream.Receive() {
				msg := stream.Msg()
				switch v := msg.Message.(type) {
				case *civ1.ExecuteCommandResponse_Stdout:
					fmt.Fprint(os.Stdout, v.Stdout)
				case *civ1.ExecuteCommandResponse_Stderr:
					fmt.Fprint(os.Stderr, v.Stderr)
				case *civ1.ExecuteCommandResponse_ExitCode:
					if v.ExitCode != 0 {
						os.Exit(int(v.ExitCode))
					}
					return nil
				}
			}
			if err := stream.Err(); err != nil {
				return fmt.Errorf("stream error: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().String("sandbox-id", "", "ID of the compute to execute the command against")
	cmd.Flags().String("session-id", "", "The session the compute belongs to")
	cmd.Flags().Int("timeout", 0, "The execution timeout in milliseconds")

	return cmd
}
