package sandbox

import (
	"fmt"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
	"github.com/depot/cli/pkg/sandbox"
	"github.com/spf13/cobra"
)

// newSandboxStop builds the `stop` command, which gracefully shuts down a
// sandbox. `down` is kept as an alias for backward compatibility.
//
// When --file is provided, the CLI runs that spec's on.down hooks against the
// sandbox before requesting the stop. Pass --no-hook to skip hooks.
func newSandboxStop() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "stop [<sandbox-id>...]",
		Aliases: []string{"down"},
		Short:   "Gracefully stop a sandbox",
		Long: `Stop a sandbox via depot.sandbox.v1.StopSandbox (terminated_by=GRACEFUL).

With --file, runs that spec's on.down hooks against each sandbox before
requesting a stop. Pass --no-hook to skip hooks.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			file, _ := cmd.Flags().GetString("file")
			setPairs, _ := cmd.Flags().GetStringArray("set")
			noHook, _ := cmd.Flags().GetBool("no-hook")
			blocking, _ := cmd.Flags().GetBool("blocking")

			ids := args

			var downHooks []sandbox.HookSpec
			if !noHook && file != "" {
				hooks, err := resolveStageHooks(file, "on.down", setPairs, func(s *sandbox.Spec) []sandbox.HookSpec {
					return s.On.Down
				})
				if err != nil {
					return fmt.Errorf("resolve on.down: %w", err)
				}
				downHooks = hooks
			}
			if !noHook && file == "" && len(setPairs) > 0 {
				return fmt.Errorf("on.down --set requires --file")
			}

			token, orgID, err := resolveAuthAndOrg(ctx, cmd)
			if err != nil {
				return err
			}

			client := api.NewSandboxV0Client()
			var failures []string
			for _, id := range ids {
				if len(downHooks) > 0 {
					if err := runHooks(ctx, client, token, orgID, id, "on.down", sandboxv1.HookStage_HOOK_STAGE_DOWN, downHooks, os.Stdout, os.Stderr); err != nil {
						failures = append(failures, fmt.Sprintf("%s: on.down: %v", id, err))
						continue
					}
				}
				req := &sandboxv1.StopSandboxRequest{Sandbox: sandboxRef(id)}
				if blocking {
					b := true
					req.Blocking = &b
				}
				_, err := client.StopSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
				if err != nil {
					failures = append(failures, fmt.Sprintf("%s: stop: %v", id, err))
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "stopped %s\n", id)
			}
			if len(failures) > 0 {
				return fmt.Errorf("stop failed:\n  %s", strings.Join(failures, "\n  "))
			}
			return nil
		},
	}
	cmd.Flags().Bool("blocking", false, "Block until the sandbox reaches terminal state (subject to server-side cap)")
	addHookFlags(cmd, "on.down")
	return cmd
}
