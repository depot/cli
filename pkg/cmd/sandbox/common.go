package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
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

// sandboxRef wraps a sandbox id in the depot.sandbox.v1 selector oneof.
// Every `<sandbox-id>` positional argument that hits the v0 wire ends up in
// SandboxRef{Selector: SandboxRef_Id{Id: id}} — this is the single helper.
func sandboxRef(id string) *sandboxv1.SandboxRef {
	return &sandboxv1.SandboxRef{
		Selector: &sandboxv1.SandboxRef_Id{Id: id},
	}
}

// consumeCommandEventStream drains a depot.sandbox.v1 CommandEvent stream
// into stdout/stderr and returns the final exit code from Finished. The
// stream shape mirrors RunCommand / RunCommandPipe / AttachCommand /
// RunHook: Started -> Stdout/Stderr/Error/EvictedEarlyData* -> Finished.
//
// EvictedEarlyData is reported on stderr as a single line so log consumers
// see the gap; the stream continues afterward. Error frames abort so callers
// do not treat degraded output as a successful command.
func consumeCommandEventStream(
	stream *connect.ServerStreamForClient[sandboxv1.SandboxCommandExecutionEvent],
	stdout, stderr io.Writer,
) (exitCode int32, err error) {
	defer func() { _ = stream.Close() }()
	for stream.Receive() {
		msg := stream.Msg()
		switch ev := msg.Event.(type) {
		case *sandboxv1.SandboxCommandExecutionEvent_Started_:
			// metadata only — nothing to print
		case *sandboxv1.SandboxCommandExecutionEvent_Stdout:
			if ev.Stdout != nil && len(ev.Stdout.Data) > 0 {
				if _, err := stdout.Write(ev.Stdout.Data); err != nil {
					return 0, fmt.Errorf("write stdout: %w", err)
				}
			}
		case *sandboxv1.SandboxCommandExecutionEvent_Stderr:
			if ev.Stderr != nil && len(ev.Stderr.Data) > 0 {
				if _, err := stderr.Write(ev.Stderr.Data); err != nil {
					return 0, fmt.Errorf("write stderr: %w", err)
				}
			}
		case *sandboxv1.SandboxCommandExecutionEvent_Finished_:
			if ev.Finished != nil {
				return ev.Finished.ExitCode, nil
			}
		case *sandboxv1.SandboxCommandExecutionEvent_Error_:
			if ev.Error != nil {
				if _, err := fmt.Fprintf(stderr, "[command-error] %s\n", ev.Error.Reason); err != nil {
					return 0, fmt.Errorf("write stderr: %w", err)
				}
				return 0, fmt.Errorf("command error: %s", ev.Error.Reason)
			}
		case *sandboxv1.SandboxCommandExecutionEvent_Evicted:
			if ev.Evicted != nil {
				if _, err := fmt.Fprintf(stderr, "[evicted-early-data] dropped %d bytes stdout / %d bytes stderr\n",
					ev.Evicted.DroppedBytesStdout, ev.Evicted.DroppedBytesStderr); err != nil {
					return 0, fmt.Errorf("write stderr: %w", err)
				}
			}
		}
	}
	if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("command stream: %w", err)
	}
	return 0, fmt.Errorf("command stream closed without Finished event")
}
