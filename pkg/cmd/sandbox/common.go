package sandbox

import (
	"context"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
	"github.com/depot/cli/pkg/sandbox"
	"github.com/spf13/cobra"
)

// resolveAuthAndOrg pulls the persistent --token / --org flags off cmd and
// returns the fully-resolved DEPOT_TOKEN + organization id every sandbox
// subcommand needs.
func resolveAuthAndOrg(ctx context.Context, cmd *cobra.Command) (token, orgID string, err error) {
	token, _ = cmd.Flags().GetString("token")
	token, err = helpers.ResolveOrgAuth(ctx, token)
	if err != nil {
		return "", "", fmt.Errorf("resolve token: %w", err)
	}
	orgID, _ = cmd.Flags().GetString("org")
	if orgID == "" {
		orgID = config.GetCurrentOrganization()
	}
	return token, orgID, nil
}

// addHookFlags declares the --file / --set / --no-hook triple every hook-aware
// command shares. stageLabel ("on.exec", "on.shell", …) is interpolated into
// the help text so `--help` reads naturally.
func addHookFlags(cmd *cobra.Command, stageLabel string) {
	cmd.Flags().StringP("file", "f", "", fmt.Sprintf("Path to a sandbox.depot.yml file for %s resolution (default: walk up from cwd)", stageLabel))
	cmd.Flags().StringArray("set", nil, fmt.Sprintf("Inputs as KEY=VALUE for %s ${input.KEY} substitution; repeatable", stageLabel))
	cmd.Flags().Bool("no-hook", false, fmt.Sprintf("Skip %s hooks declared in the spec", stageLabel))
}

// runHookStage reads the standard --file/--set/--no-hook flags, resolves the
// named stage from the local spec, and runs it against (sandboxID, sessionID).
// A --no-hook flag short-circuits to a no-op. pick selects which stage's
// hooks to fire out of the resolved HooksSpec.
func runHookStage(
	ctx context.Context,
	cmd *cobra.Command,
	client civ1connect.DepotComputeServiceClient,
	token, orgID, sandboxID, sessionID, label string,
	pick func(sandbox.HooksSpec) []sandbox.HookSpec,
	stdout, stderr io.Writer,
) error {
	noHook, _ := cmd.Flags().GetBool("no-hook")
	if noHook {
		return nil
	}
	file, _ := cmd.Flags().GetString("file")
	setPairs, _ := cmd.Flags().GetStringArray("set")
	hooks, err := resolveStageHooks(file, setPairs)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", label, err)
	}
	return runHooks(ctx, client, token, orgID, sandboxID, sessionID, label, pick(hooks), stdout, stderr)
}

// consumeRemoteExec drains a RemoteExec stream into stdout/stderr, preferring
// the binary-safe StdoutRaw/StderrRaw rails (DEP-4404) and falling back to
// the legacy oneof variants when the server hasn't populated raw. Returns the
// process's exit code once the server emits an ExitCode message; the caller
// decides whether a non-zero exit is fatal.
func consumeRemoteExec(
	stream *connect.ServerStreamForClient[civ1.ExecuteCommandResponse],
	stdout, stderr io.Writer,
) (exitCode int32, err error) {
	for stream.Receive() {
		msg := stream.Msg()
		if len(msg.StdoutRaw) > 0 {
			_, _ = stdout.Write(msg.StdoutRaw)
		}
		if len(msg.StderrRaw) > 0 {
			_, _ = stderr.Write(msg.StderrRaw)
		}
		switch v := msg.Message.(type) {
		case *civ1.ExecuteCommandResponse_Stdout:
			if len(msg.StdoutRaw) == 0 {
				fmt.Fprintln(stdout, v.Stdout)
			}
		case *civ1.ExecuteCommandResponse_Stderr:
			if len(msg.StderrRaw) == 0 {
				fmt.Fprintln(stderr, v.Stderr)
			}
		case *civ1.ExecuteCommandResponse_ExitCode:
			return v.ExitCode, nil
		}
	}
	if err := stream.Err(); err != nil {
		return 0, fmt.Errorf("stream: %w", err)
	}
	return 0, nil
}
