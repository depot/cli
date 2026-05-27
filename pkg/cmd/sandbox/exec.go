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

// newSandboxExec wraps depot.sandbox.v1.SandboxService.RunCommand — execute
// one command in an existing sandbox and stream the CommandEvent rail.
//
// M34 retires the legacy --sess flag (D-M34-M) — sandboxv1 takes a SandboxRef only.
// M34 deprecates --timeout (MD-7); the flag is hidden + ignored, removed in a
// follow-on slice.
func newSandboxExec() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec [flags] <sandbox-id> -- <command> [args...]",
		Short: "Execute a command within a running sandbox",
		Long: `Run a one-off command inside a sandbox via depot.sandbox.v1.RunCommand.

The command and its args follow a -- separator from cobra's flag set so flag
parsing stops there.`,
		Example: `
  # Run whoami
  depot sandbox exec cs-abc123 -- /bin/bash -lc whoami

  # Streaming loop
  depot sandbox exec cs-abc123 -- /bin/bash -lc 'for i in {1..10}; do echo $i; sleep 1; done'
`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token, orgID, err := resolveAuthAndOrg(ctx, cmd)
			if err != nil {
				return err
			}

			sandboxID := args[0]
			cmdArgs := args[1:]

			client := api.NewSandboxV0Client()

			if err := runHookStage(ctx, cmd, client, token, orgID, sandboxID, "on.exec",
				func(h sandbox.HooksSpec) []sandbox.HookSpec { return h.Exec }, os.Stdout, os.Stderr); err != nil {
				return err
			}

			cwd, _ := cmd.Flags().GetString("cwd")
			envSlice, _ := cmd.Flags().GetStringArray("env")
			sudo, _ := cmd.Flags().GetBool("sudo")
			detached, _ := cmd.Flags().GetBool("detached")

			envMap, err := parseEnvSlice(envSlice)
			if err != nil {
				return err
			}

			req := &sandboxv1.RunCommandRequest{
				Sandbox: sandboxRef(sandboxID),
				Cmd:     cmdArgs[0],
				Args:    cmdArgs[1:],
				Env:     envMap,
			}
			if cwd != "" {
				req.Cwd = &cwd
			}
			if sudo {
				req.Sudo = &sudo
			}
			if detached {
				req.Detached = &detached
			}

			// --timeout is deprecated (MD-7). Warn if anyone still sets it,
			// but don't fail — the wire field is gone in v0.
			if t, _ := cmd.Flags().GetInt("timeout"); t > 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: --timeout is deprecated and ignored on the v0 wire; remove it (will be deleted in a follow-on slice)")
			}

			stream, err := client.RunCommand(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
			if err != nil {
				return fmt.Errorf("run command: %w", err)
			}

			mode := streamUntilFinished
			if detached {
				mode = streamUntilStarted
			}
			exit, err := consumeCommandEventStream(stream, os.Stdout, os.Stderr, mode)
			if err != nil {
				return err
			}
			if exit != 0 {
				os.Exit(int(exit))
			}
			return nil
		},
	}

	cmd.Flags().String("cwd", "", "Working directory inside the sandbox")
	cmd.Flags().StringArray("env", nil, "Environment variables to set (KEY=VALUE), repeatable")
	cmd.Flags().Bool("sudo", false, "Run as root")
	cmd.Flags().Bool("detached", false, "Return after Started; reattach later via AttachCommand")
	// Deprecated — hidden + ignored. Removed in a follow-on slice (MD-7).
	cmd.Flags().Int("timeout", 0, "Deprecated: timeouts are not part of the v0 wire (will be removed)")
	_ = cmd.Flags().MarkHidden("timeout")
	addHookFlags(cmd, "on.exec")

	return cmd
}

// parseEnvSlice converts ["KEY=VALUE", ...] into a map; rejects entries with
// no '='. Shared by exec / exec-pipe / shell.
func parseEnvSlice(in []string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for _, e := range in {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			return nil, fmt.Errorf("invalid env format %q, expected KEY=VALUE", e)
		}
		out[k] = v
	}
	return out, nil
}
