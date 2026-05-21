package sandbox

import (
	"fmt"
	"os"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/sandbox"
	"github.com/spf13/cobra"
)

// `down` is a graceful version of `kill`: run the spec's on.down hooks
// first, then terminate the sandbox. Use it from inside a spec dir to
// flush state, sync work back, or save logs before the VM disappears.
func newSandboxDown() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "down [<sandbox-id>...]",
		Short: "Run on.down hooks, then terminate the sandbox",
		Long: `Run the spec's on.down hooks against the sandbox, then kill it.

With no arguments, walks up from cwd for a sandbox.depot.yml, picks up
the sandbox last started under that spec's name, runs on.down hooks
declared in the spec, and kills it.

With one or more sandbox ids, runs on.down (resolved from the local
spec, if one is found via cwd or --file) against each, then kills
each. Pass --no-hook to skip the hooks and just kill, matching what
plain "sandbox kill" does.`,
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

			ids := args
			if len(ids) == 0 {
				id, err := sandboxIDFromLocalSpec(file)
				if err != nil {
					return err
				}
				ids = []string{id}
			}

			// Resolve on.down hooks from the local spec (if any). When
			// the user passed a raw id and no spec is reachable, we just
			// kill — matches `sandbox kill` semantics in that case.
			var downHooks []sandbox.HookSpec
			if !noHook {
				hooks, err := resolveStageHooks(file, setPairs)
				if err != nil {
					return fmt.Errorf("resolve on.down: %w", err)
				}
				downHooks = hooks.Down
			}

			sandboxClient := api.NewSandboxClient()
			computeClient := api.NewComputeClient()
			var failures []string
			for _, id := range ids {
				if len(downHooks) > 0 {
					sessionID, err := resolveSession(ctx, id, token, orgID)
					if err != nil {
						failures = append(failures, fmt.Sprintf("%s: resolve session: %v", id, err))
						continue
					}
					if err := runHooks(ctx, computeClient, token, orgID, id, sessionID, "on.down", downHooks, os.Stdout, os.Stderr); err != nil {
						failures = append(failures, fmt.Sprintf("%s: on.down: %v", id, err))
						continue
					}
				}
				_, err := sandboxClient.KillSandbox(ctx, api.WithAuthenticationAndOrg(
					connect.NewRequest(&agentv1.KillSandboxRequest{SandboxId: id}), token, orgID))
				if err != nil {
					failures = append(failures, fmt.Sprintf("%s: kill: %v", id, err))
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "killed %s\n", id)
			}
			if len(failures) > 0 {
				return fmt.Errorf("down failed:\n  %s", failures)
			}
			return nil
		},
	}
	addHookFlags(cmd, "on.down")
	return cmd
}
