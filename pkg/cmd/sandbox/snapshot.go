package sandbox

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

// newSandboxSnapshot tars the sandbox's live root filesystem into a scratch
// directory inside the sandbox, then runs `snapshot build-ext4` on that
// scratch dir to produce a fresh, standalone ext4 image and push it to the
// registry. The result has the same shape as what `sandbox build` produces,
// so booting from it via spec.Image works with the existing StartSandbox
// path — no api or vm3 changes required.
//
// (depot/snapshot-action takes the sparser thin-compose path which uploads
// only the dm-thin diff over a base. We avoid it here because that path
// needs `thin_dump` from thin-provisioning-tools in the sandbox, plus an
// api/vm3 layered-image consumer that doesn't exist for sandboxes today.
// Trade: bigger uploads, simpler boot.)
//
// Tools assumed in the sandbox: curl, jq, tar (to fetch and unpack the
// snapshot binary). Runs as the sandbox's root.
func newSandboxSnapshot() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot <sandbox-id> <image-ref>",
		Short: "Snapshot a running sandbox to a registry image",
		Long: `Capture the live filesystem state of a running sandbox and push it to the
registry as a standalone ext4 image. The result is shaped like what
"depot sandbox build" produces, so a subsequent "depot sandbox up" against
a spec with this ref under "image:" (and no [container.build] section)
boots from the snapshot.

Tars / inside the sandbox to a scratch directory (skipping pseudo-fs
mounts via --one-file-system + an explicit /tmp exclude), then runs
"snapshot build-ext4 --source-dir <scratch> --registry <ref>".

The sandbox needs curl, jq, and tar on PATH. Pass --skip-install if you've
already baked the snapshot binary into the image and want to skip the
download.`,
		Example: `
  # Snapshot a sandbox into an org-tenant tag.
  depot sandbox snapshot cs-abc123 \
    <orgID>.registry.depot.dev/<projectID>:my-snap
`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sandboxID := args[0]
			destRef := args[1]

			token, _ := cmd.Flags().GetString("token")
			token, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return fmt.Errorf("resolve token: %w", err)
			}
			orgID, _ := cmd.Flags().GetString("org")
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			sb := api.NewSandboxClient()
			res, err := sb.GetSandbox(ctx, api.WithAuthenticationAndOrg(
				connect.NewRequest(&agentv1.GetSandboxRequest{SandboxId: sandboxID}), token, orgID))
			if err != nil {
				return fmt.Errorf("get sandbox: %w", err)
			}
			if res.Msg.Sandbox == nil {
				return fmt.Errorf("sandbox %s not found", sandboxID)
			}
			sessionID := res.Msg.Sandbox.SessionId

			ver, _ := cmd.Flags().GetString("snapshot-version")
			if ver == "" {
				ver = snapshotVersion // defined in convert.go
			}
			skipInstall, _ := cmd.Flags().GetBool("skip-install")
			binDir, _ := cmd.Flags().GetString("bin-dir")
			if binDir == "" {
				binDir = "/tmp/depot-snap"
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Snapshotting (sandbox: %s)\n  destination: %s\n", sandboxID, destRef)

			// Args (positional in /bin/sh -c <script> $0 $1 $2 $3 $4):
			//   $0 = "depot-snap" (script name placeholder)
			//   $1 = DEPOT_TOKEN (registry password)
			//   $2 = destRef
			//   $3 = snapshot version
			//   $4 = install dir
			//   $5 = "1" if skip-install
			script := `set -e
INSTALL_DIR="$4"
if [ "$5" != "1" ]; then
  mkdir -p "$INSTALL_DIR"
  url=$(curl -fsSL "https://dl.depot.dev/snapshot/release/linux/x64/$3" | jq -r .url)
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
env REGISTRY_USERNAME=x-token REGISTRY_PASSWORD="$1" \
  "$INSTALL_DIR/snapshot" build-ext4 \
    --source-dir "$SCRATCH" \
    --output "$OUTPUT" \
    --registry "$2"
rm -rf "$SCRATCH" "$OUTPUT"
`
			skipFlag := "0"
			if skipInstall {
				skipFlag = "1"
			}

			client := api.NewComputeClient()
			stream, err := client.RemoteExec(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(&civ1.ExecuteCommandRequest{
				SandboxId: sandboxID,
				SessionId: sessionID,
				Command: &civ1.Command{
					CommandArray: []string{"/bin/sh", "-c", script, "depot-snap", token, destRef, ver, binDir, skipFlag},
					TimeoutMs:    int32(15 * 60 * 1000),
				},
			}), token, orgID))
			if err != nil {
				return fmt.Errorf("exec: %w", err)
			}

			stdout := cmd.OutOrStdout()
			stderr := cmd.ErrOrStderr()
			for stream.Receive() {
				switch v := stream.Msg().Message.(type) {
				case *civ1.ExecuteCommandResponse_Stdout:
					fmt.Fprint(stdout, v.Stdout)
				case *civ1.ExecuteCommandResponse_Stderr:
					fmt.Fprint(stderr, v.Stderr)
				case *civ1.ExecuteCommandResponse_ExitCode:
					if v.ExitCode != 0 {
						return fmt.Errorf("snapshot thin-compose exited %d", v.ExitCode)
					}
					fmt.Fprintf(stdout, "\nSaved %s\n", destRef)
					return nil
				}
			}
			if err := stream.Err(); err != nil {
				return fmt.Errorf("stream: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().String("snapshot-version", "", "Override the snapshot binary version (default: pinned in cli)")
	cmd.Flags().String("bin-dir", "", "Override the snapshot binary install dir inside the sandbox (default: /tmp/depot-snap)")
	cmd.Flags().Bool("skip-install", false, "Skip downloading the snapshot binary (assumes it's already at --bin-dir/snapshot)")
	return cmd
}
