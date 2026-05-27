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

// commandRef wraps a command id in the depot.sandbox.v1 selector oneof.
// Used by AttachCommand / KillCommand callers.
func commandRef(id string) *sandboxv1.CommandRef {
	return &sandboxv1.CommandRef{
		Selector: &sandboxv1.CommandRef_Id{Id: id},
	}
}

// snapshotRef wraps a snapshot id in the depot.sandbox.v1 selector oneof.
// Used by GetSnapshot / DeleteSnapshot.
func snapshotRef(id string) *sandboxv1.SnapshotRef {
	return &sandboxv1.SnapshotRef{
		Selector: &sandboxv1.SnapshotRef_Id{Id: id},
	}
}

// consumeCommandEventStream drains a depot.sandbox.v1 CommandEvent stream
// into stdout/stderr and returns the final exit code from Finished. The
// stream shape mirrors RunCommand / RunCommandPipe / AttachCommand /
// RunHook: Started -> Stdout/Stderr/Error/EvictedEarlyData* -> Finished.
//
// EvictedEarlyData is reported on stderr as a single line so log consumers
// see the gap; the stream continues afterward. Error frames are surfaced the
// same way and do not abort the loop (the server is signalling partial
// degradation, not a fatal end — Connect transport errors are the fatal path).
func consumeCommandEventStream(
	stream *connect.ServerStreamForClient[sandboxv1.CommandEvent],
	stdout, stderr io.Writer,
) (exitCode int32, err error) {
	defer func() { _ = stream.Close() }()
	for stream.Receive() {
		msg := stream.Msg()
		switch ev := msg.Event.(type) {
		case *sandboxv1.CommandEvent_Started_:
			// metadata only — nothing to print
		case *sandboxv1.CommandEvent_Stdout:
			if ev.Stdout != nil && len(ev.Stdout.Data) > 0 {
				_, _ = stdout.Write(ev.Stdout.Data)
			}
		case *sandboxv1.CommandEvent_Stderr:
			if ev.Stderr != nil && len(ev.Stderr.Data) > 0 {
				_, _ = stderr.Write(ev.Stderr.Data)
			}
		case *sandboxv1.CommandEvent_Finished_:
			if ev.Finished != nil {
				return ev.Finished.ExitCode, nil
			}
		case *sandboxv1.CommandEvent_Error_:
			if ev.Error != nil {
				fmt.Fprintf(stderr, "[command-error] %s\n", ev.Error.Reason)
			}
		case *sandboxv1.CommandEvent_Evicted:
			if ev.Evicted != nil {
				fmt.Fprintf(stderr, "[evicted-early-data] dropped %d bytes stdout / %d bytes stderr\n",
					ev.Evicted.DroppedBytesStdout, ev.Evicted.DroppedBytesStderr)
			}
		}
	}
	if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("command stream: %w", err)
	}
	return 0, fmt.Errorf("command stream ended without Finished event")
}
