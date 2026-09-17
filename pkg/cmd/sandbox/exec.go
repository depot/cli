package sandbox

import (
	"context"
	"fmt"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
	"github.com/depot/cli/pkg/sandbox"
	"github.com/spf13/cobra"
)

// newSandboxExec builds the `exec` command, which runs a single command in an
// existing sandbox and streams the command's output events back to the caller.
//
// The command targets a sandbox by id. The --timeout flag is deprecated: it is
// hidden and ignored, since timeouts are not part of the current wire protocol.
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
		Args: func(cmd *cobra.Command, args []string) error {
			_, _, err := sandboxExecTarget(cmd, args)
			return err
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token, orgID, err := resolveAuthAndOrg(ctx, cmd)
			if err != nil {
				return err
			}

			if legacySandboxID, _ := cmd.Flags().GetString("sandbox-id"); legacySandboxID != "" {
				return runLegacySandboxExec(ctx, cmd, args, token, orgID, legacySandboxID)
			}

			sandboxID, cmdArgs, err := sandboxExecTarget(cmd, args)
			if err != nil {
				return err
			}

			client := api.NewSandboxV0Client()

			if err := runHookStage(ctx, cmd, client, token, orgID, sandboxID, "on.exec",
				sandboxv1.HookStage_HOOK_STAGE_EXEC,
				func(s *sandbox.Spec) []sandbox.HookSpec { return s.On.Exec }, os.Stdout, os.Stderr); err != nil {
				return err
			}

			cwd, _ := cmd.Flags().GetString("cwd")
			envSlice, _ := cmd.Flags().GetStringArray("env")
			sudo, _ := cmd.Flags().GetBool("sudo")

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

			// The --timeout flag is deprecated for the positional v0 path. The
			// legacy --sandbox-id path still preserves timeout semantics.
			if t, _ := cmd.Flags().GetInt("timeout"); t > 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: --timeout is deprecated and ignored on the v0 wire; remove it (will be deleted in a follow-on slice)")
			}

			stream, err := client.RunCommand(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
			if err != nil {
				return fmt.Errorf("run command: %w", err)
			}

			exit, err := consumeCommandEventStream(stream, os.Stdout, os.Stderr)
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
	cmd.Flags().String("sandbox-id", "", "ID of the compute to execute the command against")
	cmd.Flags().String("session-id", "", "The session the compute belongs to")
	// Deprecated: hidden and ignored.
	cmd.Flags().Int("timeout", 0, "Deprecated: timeouts are not part of the v0 wire (will be removed)")
	_ = cmd.Flags().MarkHidden("timeout")
	addHookFlags(cmd, "on.exec")

	return cmd
}

func runLegacySandboxExec(ctx context.Context, cmd *cobra.Command, args []string, token, orgID, sandboxID string) error {
	if len(args) < 1 {
		return fmt.Errorf("requires a command after -- when --sandbox-id is used")
	}
	sessionID, _ := cmd.Flags().GetString("session-id")
	if sessionID == "" {
		return fmt.Errorf("session-id is required")
	}
	timeout, _ := cmd.Flags().GetInt("timeout")

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
		return sandboxExecError(err, sandboxID)
	}

	for stream.Receive() {
		msg := stream.Msg()
		exitCode, exited, err := writeExecuteCommandResponse(msg, os.Stdout, os.Stderr)
		if err != nil {
			return err
		}
		if exited {
			if exitCode != 0 {
				os.Exit(int(exitCode))
			}
			return nil
		}
	}
	if err := stream.Err(); err != nil {
		return sandboxExecStreamError(err, sandboxID)
	}

	return nil
}

func sandboxExecTarget(cmd *cobra.Command, args []string) (string, []string, error) {
	legacySandboxID, _ := cmd.Flags().GetString("sandbox-id")
	if legacySandboxID != "" {
		if len(args) < 1 {
			return "", nil, fmt.Errorf("requires a command after -- when --sandbox-id is used")
		}
		return legacySandboxID, args, nil
	}
	if err := cobra.MinimumNArgs(2)(cmd, args); err != nil {
		return "", nil, err
	}
	return args[0], args[1:], nil
}

// parseEnvSlice converts a list of "KEY=VALUE" strings into a map, rejecting
// any entry that has no '='.
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
