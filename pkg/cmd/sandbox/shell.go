package sandbox

import (
	"fmt"
	"strings"

	"github.com/depot/cli/pkg/pty"
	"github.com/spf13/cobra"
)

// newSandboxShell wraps DepotComputeService.OpenPtySession as an interactive
// shell. Auth rides the standard connectrpc path (DEPOT_TOKEN + x-depot-org),
// so multi-org users work without ceremony — no SSH bastion, no bastion-shaped
// auth gymnastics. The `pty` alias preserves the older command name.
func newSandboxShell() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "shell <sandbox-id>",
		Aliases: []string{"pty"},
		Short:   "Open an interactive shell in a sandbox",
		Long: `Open an interactive shell in a running sandbox via the compute exec channel.
Authentication uses your DEPOT_TOKEN + organization context — no SSH keys, no bastion.

The session id is resolved from the sandbox id automatically. Pass --session-id
to skip the lookup if you already have one.`,
		Example: `
  # Open an interactive shell
  depot sandbox shell cs-abc123

  # Use a session id directly (skip the sandbox→session lookup)
  depot sandbox shell --session-id ses-xyz

  # Workdir + env
  depot sandbox shell cs-abc123 --cwd /workspace --env LOG_LEVEL=debug
`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token, orgID, err := resolveAuthAndOrg(ctx, cmd)
			if err != nil {
				return err
			}

			sandboxID, _ := cmd.Flags().GetString("sandbox-id")
			if sandboxID == "" && len(args) > 0 {
				sandboxID = args[0]
			}
			sessionID, _ := cmd.Flags().GetString("session-id")
			if sessionID == "" {
				if sandboxID == "" {
					return fmt.Errorf("provide a sandbox id or --session-id")
				}
				sessionID, err = resolveSession(ctx, sandboxID, token, orgID)
				if err != nil {
					return err
				}
			}

			cwd, _ := cmd.Flags().GetString("cwd")
			envSlice, _ := cmd.Flags().GetStringArray("env")

			envMap := make(map[string]string, len(envSlice))
			for _, e := range envSlice {
				k, v, ok := strings.Cut(e, "=")
				if !ok {
					return fmt.Errorf("invalid env format %q, expected KEY=VALUE", e)
				}
				envMap[k] = v
			}

			var stdinPrefix []byte
			noHook, _ := cmd.Flags().GetBool("no-hook")
			if !noHook {
				file, _ := cmd.Flags().GetString("file")
				setPairs, _ := cmd.Flags().GetStringArray("set")
				hooks, err := resolveStageHooks(file, setPairs)
				if err != nil {
					return err
				}
				if prefix := joinShellHooks(hooks.Shell); prefix != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "[on.shell] %s", prefix)
					stdinPrefix = []byte(prefix)
				}
			}

			return pty.Run(ctx, pty.SessionOptions{
				Token:       token,
				OrgID:       orgID,
				SandboxID:   sandboxID,
				SessionID:   sessionID,
				Cwd:         cwd,
				Env:         envMap,
				StdinPrefix: stdinPrefix,
			})
		},
	}

	cmd.Flags().String("sandbox-id", "", "Sandbox id (positional argument also accepted)")
	cmd.Flags().String("session-id", "", "Skip the sandbox→session lookup and use this session id directly")
	cmd.Flags().String("cwd", "", "Workdir within the sandbox. Defaults to the user's home directory.")
	cmd.Flags().StringArray("env", nil, "Environment variables to set (KEY=VALUE), repeatable")
	addHookFlags(cmd, "on.shell")

	return cmd
}
