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

// newSandboxStop wraps depot.sandbox.v1.SandboxService.StopSandbox — the
// graceful shutdown verb (terminated_by=GRACEFUL). M34 renames the legacy
// `down` to `stop` to match the SDK vocabulary; `down` survives as a cobra
// alias for script-compat (D-M34-O).
//
// With no positional args, walks up from cwd for a sandbox.depot.yml and
// resolves the most-recent sandbox started under that spec name (per
// ~/.depot/sandbox-state/<name>.id). With one or more ids, stops each.
//
// Hook behavior: on.down hooks are still CLI-side (D-M34-I) — they run via
// RunCommand against the sandboxv1 wire before the StopSandbox call. Pass
// --no-hook to skip and stop immediately.
func newSandboxStop() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "stop [<sandbox-id>...]",
		Aliases: []string{"down"},
		Short:   "Gracefully stop a sandbox (runs on.down hooks first)",
		Long: `Stop a sandbox via depot.sandbox.v1.StopSandbox (terminated_by=GRACEFUL).

With no arguments, walks up from cwd for a sandbox.depot.yml, picks up the
sandbox last started under that spec's name, runs on.down hooks, then asks
the server to stop it.

With one or more sandbox ids, runs on.down (resolved from the local spec, if
one is found) against each, then stops each. Pass --no-hook to skip the
hooks and stop immediately.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			token, orgID, err := resolveAuthAndOrg(ctx, cmd)
			if err != nil {
				return err
			}

			file, _ := cmd.Flags().GetString("file")
			setPairs, _ := cmd.Flags().GetStringArray("set")
			noHook, _ := cmd.Flags().GetBool("no-hook")
			blocking, _ := cmd.Flags().GetBool("blocking")

			ids := args
			if len(ids) == 0 {
				id, err := sandboxIDFromLocalSpec(file)
				if err != nil {
					return err
				}
				ids = []string{id}
			}

			var downHooks []sandbox.HookSpec
			if !noHook {
				hooks, err := resolveStageHooks(file, setPairs)
				if err != nil {
					return fmt.Errorf("resolve on.down: %w", err)
				}
				downHooks = hooks.Down
			}

			client := api.NewSandboxV0Client()
			var failures []string
			for _, id := range ids {
				if len(downHooks) > 0 {
					if err := runHooks(ctx, client, token, orgID, id, "on.down", downHooks, os.Stdout, os.Stderr); err != nil {
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
