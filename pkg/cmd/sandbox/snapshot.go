package sandbox

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
	"github.com/depot/cli/pkg/sandbox"
	"github.com/spf13/cobra"
)

// snapshotVersion pins the `snapshot` binary the verb downloads into the
// sandbox by default. Override per-invocation with --snapshot-version. Bump
// this when a newer snapshot release is required across the board.
const snapshotVersion = "1.2.16"

// newSandboxSnapshot tars the sandbox's live root filesystem into a scratch
// directory inside the sandbox, then runs `snapshot build-ext4` on that
// scratch dir to produce a fresh, standalone ext4 image and push it to the
// registry. The result has the same shape as what `depot sandbox build`
// produces, so booting from it via a spec's image works with the existing
// StartSandbox path — no api or vm3 changes required.
//
// Transport: this verb dials the same depot.sandbox.v1 RunCommand rail that
// `depot sandbox exec` uses — a single `/bin/sh -c <script>` invocation keyed
// on the sandbox id. It deliberately does NOT use the Depot-CI compute path
// (ExecuteCommandRequest / RemoteExec / SessionId): the sandbox rail needs no
// session, only the sandbox id.
//
// Known fragility (DEP-4919, not addressed here): the snapshot is a
// long-running exec (build-ext4 over a full rootfs — budget for ~15 minutes)
// streamed through the api's RunCommand passthrough, so an api deploy mid-run
// drops the stream. That is acceptable for v0; do not try to solve the
// streaming path in this verb.
//
// Tools assumed in the sandbox: curl, jq, tar (to fetch and unpack the
// snapshot binary). Runs as the sandbox's root.
func newSandboxSnapshot() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot <sandbox-id> <image-ref>",
		Short: "Snapshot a running sandbox to a registry image",
		Long: `Capture the live filesystem state of a running sandbox and push it to the
registry as a standalone ext4 image. The result is shaped like what
"depot sandbox build" produces, so a subsequent boot against a spec with this
ref under "image:" (and no [container.build] section) boots from the snapshot.

Tars / inside the sandbox to a scratch directory (skipping pseudo-fs mounts via
--one-file-system plus an explicit /tmp exclude), then runs
"snapshot build-ext4 --source-dir <scratch> --registry <ref>".

The sandbox needs curl, jq, and tar on PATH. Pass --skip-install if you've
already baked the snapshot binary into the image and want to skip the
download.`,
		Example: `
  # Snapshot a sandbox into a registry tag.
  depot sandbox snapshot cs-abc123 \
    <orgID>.registry.depot.dev/<projectID>:my-snap
`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sandboxID := args[0]
			destRef := args[1]

			token, orgID, err := resolveAuthAndOrg(ctx, cmd)
			if err != nil {
				return err
			}

			ver, _ := cmd.Flags().GetString("snapshot-version")
			if ver == "" {
				ver = snapshotVersion
			}
			skipInstall, _ := cmd.Flags().GetBool("skip-install")
			binDir, _ := cmd.Flags().GetString("bin-dir")
			if binDir == "" {
				binDir = "/tmp/depot-snap"
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Snapshotting (sandbox: %s)\n  destination: %s\n", sandboxID, destRef)
			fmt.Fprintf(cmd.OutOrStdout(),
				"  pipeline:    tar c -C / --one-file-system --exclude=./tmp -f - . | snapshot build-ext4 --registry %s\n",
				destRef)

			client := api.NewSandboxV0Client()

			if err := runHookStage(ctx, cmd, client, token, orgID, sandboxID, "on.snapshot",
				func(h sandbox.HooksSpec) []sandbox.HookSpec { return h.Snapshot }, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return err
			}

			// The registry password is the Depot token. We pass it through the
			// RunCommandRequest.Env map (as $REGISTRY_PASSWORD, read by the script
			// below) rather than as a positional argument, so the secret never
			// shows up in the sandbox process list (an improvement over the
			// argv-positional form earlier iterations used). REGISTRY_USERNAME is
			// the non-secret literal "x-token" and stays inline in the script.
			//
			// Non-secret positionals follow "-c" and the script name:
			//   $1 = destRef
			//   $2 = snapshot version
			//   $3 = install dir
			//   $4 = "1" if skip-install, "0" otherwise
			script := `set -e
INSTALL_DIR="$3"
if [ "$4" != "1" ]; then
  mkdir -p "$INSTALL_DIR"
  url=$(curl -fsSL "https://dl.depot.dev/snapshot/release/linux/x64/$2" | jq -r .url)
  curl -fsSL -o "$INSTALL_DIR/snapshot.tar.gz" "$url"
  tar -xzf "$INSTALL_DIR/snapshot.tar.gz" -C "$INSTALL_DIR"
fi
SCRATCH="$INSTALL_DIR/scratch"
OUTPUT="$INSTALL_DIR/snapshot.ext4"
rm -rf "$SCRATCH" "$OUTPUT"
mkdir -p "$SCRATCH"
# --one-file-system skips /proc, /sys, /dev, /run (separate mounts).
# /tmp is on the root fs so we exclude it explicitly — INSTALL_DIR
# lives there too. The new sandbox boots with a fresh /tmp anyway.
tar c -C / --one-file-system --exclude=./tmp -f - . | tar x -C "$SCRATCH" -f -
# Recreate mountpoints so the new sandbox's init can mount over them.
mkdir -p "$SCRATCH/proc" "$SCRATCH/sys" "$SCRATCH/dev" "$SCRATCH/tmp" "$SCRATCH/run"
env REGISTRY_USERNAME=x-token REGISTRY_PASSWORD="$REGISTRY_PASSWORD" \
  "$INSTALL_DIR/snapshot" build-ext4 \
    --source-dir "$SCRATCH" \
    --output "$OUTPUT" \
    --registry "$1"
rm -rf "$SCRATCH" "$OUTPUT"
`
			skipFlag := "0"
			if skipInstall {
				skipFlag = "1"
			}

			req := &sandboxv1.RunCommandRequest{
				Sandbox: sandboxRef(sandboxID),
				Cmd:     "/bin/sh",
				Args:    []string{"-c", script, "depot-snap", destRef, ver, binDir, skipFlag},
				Env:     map[string]string{"REGISTRY_PASSWORD": token},
			}

			stream, err := client.RunCommand(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
			if err != nil {
				return fmt.Errorf("run command: %w", err)
			}

			stdout := cmd.OutOrStdout()
			stderr := cmd.ErrOrStderr()
			exit, err := consumeCommandEventStream(stream, stdout, stderr, streamUntilFinished)
			if err != nil {
				return err
			}
			if exit != 0 {
				return fmt.Errorf("snapshot exited %d", exit)
			}
			fmt.Fprintf(stdout, "\nSaved %s\n", destRef)
			return nil
		},
	}
	cmd.Flags().String("snapshot-version", "", "Override the snapshot binary version (default: pinned in cli)")
	cmd.Flags().String("bin-dir", "", "Override the snapshot binary install dir inside the sandbox (default: /tmp/depot-snap)")
	cmd.Flags().Bool("skip-install", false, "Skip downloading the snapshot binary (assumes it's already at --bin-dir/snapshot)")
	addHookFlags(cmd, "on.snapshot")
	return cmd
}
