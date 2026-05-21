package sandbox

import (
	"fmt"
	"os"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/sandbox"
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

			token, orgID, err := resolveAuthAndOrg(ctx, cmd)
			if err != nil {
				return err
			}

			sandboxID, _ := cmd.Flags().GetString("sandbox-id")
			if sandboxID == "" {
				return fmt.Errorf("sandbox-id is required")
			}
			sessionID, _ := cmd.Flags().GetString("session-id")
			if sessionID == "" {
				sessionID, err = resolveSession(ctx, sandboxID, token, orgID)
				if err != nil {
					return err
				}
			}

			timeout, _ := cmd.Flags().GetInt("timeout")
			client := api.NewComputeClient()

			if err := runHookStage(ctx, cmd, client, token, orgID, sandboxID, sessionID, "on.exec",
				func(h sandbox.HooksSpec) []sandbox.HookSpec { return h.Exec }, os.Stdout, os.Stderr); err != nil {
				return err
			}

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

			exit, err := consumeRemoteExec(stream, os.Stdout, os.Stderr)
			if err != nil {
				return err
			}
			if exit != 0 {
				os.Exit(int(exit))
			}
			return nil
		},
	}

	cmd.Flags().String("sandbox-id", "", "ID of the compute to execute the command against")
	cmd.Flags().String("session-id", "", "The session the compute belongs to")
	cmd.Flags().Int("timeout", 0, "The execution timeout in milliseconds")
	addHookFlags(cmd, "on.exec")

	return cmd
}
