package sandbox

import (
	"context"
	"fmt"
	"strings"

	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

// defaultSnapshotBinURL is where the `snapshot` helper binary is fetched from
// inside the unpack sandbox.
//
// TODO(DEP-4884): confirm the real release URL. The prior mechanics lived on the
// cli branch rob/dep-4395-...-build-ext4-start, which is no longer on origin;
// reconcile this URL (and the helper's exact arg shape below) against that work
// and the deployed snapshot helper release.
const defaultSnapshotBinURL = "https://dl.depot.dev/snapshot/latest/snapshot-linux-amd64"

// defaultSnapshotBuilderImage is the image the short-lived unpack sandbox boots
// with. Empty means "let the API pick the default builder image".
//
// TODO(DEP-4884): pin the Depot default builder image used for unpack sandboxes.
const defaultSnapshotBuilderImage = ""

// newSandboxSnapshot builds `depot sandbox snapshot`, which unpacks a registry
// OCI image into an ext4 snapshot that future sandboxes can boot from.
//
// IMPORTANT (per product direction): the unpack runs via the Depot Sandbox API
// (depot.sandbox.v1) — this verb creates a short-lived sandbox and runs the
// `snapshot` helper inside it as a command exec. It deliberately does NOT use
// Depot CI to perform the snapshot, and it is a plain imperative verb (no
// sandbox.depot.yml declarative spec).
//
// Flow:
//  1. CreateSandbox via depot.sandbox.v1 (a builder image with enough disk for
//     the target image's unpacked ext4).
//  2. Fetch the `snapshot` helper into the sandbox (curl from a release URL).
//  3. RunCommand `snapshot pull <image-ref> --output <ext4> [--size <size>]`,
//     streaming progress; the helper unpacks the OCI image into an ext4 image.
//  4. Register/return the resulting ext4 as a bootable snapshot, then tear the
//     sandbox down.
//
// DRAFT (DEP-4884) — dependencies:
//   - The depot.sandbox.v1 Go client (CreateSandbox/RunCommand/Kill) is not yet
//     on cli main; it lands with the fresh sandbox verbs (DEP-4916). This verb is
//     written against the snapshotClient interface below so the design is
//     reviewable now; newSnapshotClient is wired to that client when it lands.
//   - The snapshot-registration step (4) needs a Sandbox API call (e.g. a
//     CreateSnapshot RPC) to persist the ext4 as a bootable snapshot.
func newSandboxSnapshot() *cobra.Command {
	var (
		output       string
		size         string
		snapshotBin  string
		builderImage string
	)

	cmd := &cobra.Command{
		Use:   "snapshot <image-ref> [flags]",
		Short: "Unpack a registry OCI image into an ext4 sandbox snapshot",
		Long: `Unpack a registry OCI image into an ext4 snapshot that future sandboxes can boot from.

The unpack runs inside a short-lived sandbox via the Depot Sandbox API: this
command creates a sandbox, fetches the snapshot helper, and runs it as a command
to pull and unpack the image. It does not use Depot CI, and takes plain
arguments rather than a sandbox spec file.`,
		Example: `  # unpack an image into a bootable ext4 snapshot
  depot sandbox snapshot ghcr.io/acme/app:latest --output app.ext4 --size 10GiB`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			imageRef := args[0]

			token, err := cmd.Flags().GetString("token")
			cobra.CheckErr(err)
			token, err = helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return fmt.Errorf("failed to resolve token: %w", err)
			}

			orgID, err := cmd.Flags().GetString("org")
			cobra.CheckErr(err)
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			if output == "" {
				output = defaultSnapshotOutput(imageRef)
			}

			client, err := newSnapshotClient(ctx, token, orgID)
			if err != nil {
				return err
			}

			return runSnapshot(ctx, client, snapshotParams{
				ImageRef:     imageRef,
				Output:       output,
				Size:         size,
				SnapshotBin:  snapshotBin,
				BuilderImage: builderImage,
			})
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Name for the resulting ext4 snapshot (default: derived from the image ref)")
	cmd.Flags().StringVar(&size, "size", "", "Size of the ext4 image to create (e.g. 10GiB); default sizes to the unpacked image")
	cmd.Flags().StringVar(&snapshotBin, "snapshot-binary-url", defaultSnapshotBinURL, "URL to fetch the snapshot helper binary from")
	cmd.Flags().StringVar(&builderImage, "builder-image", defaultSnapshotBuilderImage, "Image to boot the unpack sandbox with (default: the Depot default builder image)")

	return cmd
}

type snapshotParams struct {
	ImageRef     string
	Output       string
	Size         string
	SnapshotBin  string
	BuilderImage string
}

// snapshotClient is the slice of the depot.sandbox.v1 SDK that the snapshot verb
// needs. It is implemented by the real SDK-v0 client once that lands on the CLI
// (DEP-4916); until then newSnapshotClient returns a not-wired error so the
// verb's design is reviewable without a half-built client.
type snapshotClient interface {
	// CreateSandbox boots a sandbox from the given image (empty = API default)
	// and returns its ID.
	CreateSandbox(ctx context.Context, image string) (sandboxID string, err error)
	// RunCommand runs argv in the sandbox, streaming stdout/stderr to the
	// caller's stdout/stderr, and returns the command's exit code.
	RunCommand(ctx context.Context, sandboxID string, argv []string) (exitCode int, err error)
	// Kill terminates the sandbox.
	Kill(ctx context.Context, sandboxID string) error
}

func runSnapshot(ctx context.Context, client snapshotClient, p snapshotParams) error {
	sandboxID, err := client.CreateSandbox(ctx, p.BuilderImage)
	if err != nil {
		return fmt.Errorf("create unpack sandbox: %w", err)
	}
	// The unpack sandbox is single-use; always tear it down.
	defer func() { _ = client.Kill(context.Background(), sandboxID) }()

	// 1. Fetch the snapshot helper into the sandbox.
	fetch := []string{
		"/bin/sh", "-lc",
		fmt.Sprintf("curl -fsSL %s -o /usr/local/bin/snapshot && chmod +x /usr/local/bin/snapshot", snapshotShellQuote(p.SnapshotBin)),
	}
	if code, err := client.RunCommand(ctx, sandboxID, fetch); err != nil {
		return fmt.Errorf("fetch snapshot helper: %w", err)
	} else if code != 0 {
		return fmt.Errorf("fetch snapshot helper: exit %d", code)
	}

	// 2. Unpack the OCI image into an ext4 via the helper.
	//    TODO(DEP-4884): confirm the helper's subcommand + flags against the
	//    prior mechanics (rob/dep-4395) and the deployed helper.
	unpack := []string{"snapshot", "pull", p.ImageRef, "--output", p.Output}
	if p.Size != "" {
		unpack = append(unpack, "--size", p.Size)
	}
	if code, err := client.RunCommand(ctx, sandboxID, unpack); err != nil {
		return fmt.Errorf("unpack image: %w", err)
	} else if code != 0 {
		return fmt.Errorf("snapshot helper exited %d", code)
	}

	// 3. TODO(DEP-4884): persist the ext4 as a bootable snapshot via the Sandbox
	//    API (a CreateSnapshot-style call) so future sandboxes can boot from it,
	//    and print the resulting snapshot ID.
	fmt.Printf("unpacked %s -> %s in sandbox %s\n", p.ImageRef, p.Output, sandboxID)
	return nil
}

// defaultSnapshotOutput derives an ext4 filename from an image reference, e.g.
// "ghcr.io/acme/app:latest" -> "app-latest.ext4".
func defaultSnapshotOutput(imageRef string) string {
	name := imageRef
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.NewReplacer(":", "-", "@", "-").Replace(name)
	if name == "" {
		name = "snapshot"
	}
	return name + ".ext4"
}

// snapshotShellQuote single-quotes a string for safe interpolation into a
// `/bin/sh -lc` command line.
func snapshotShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// newSnapshotClient constructs the depot.sandbox.v1 SDK client the verb runs
// against. The Go client is not yet on cli main — it lands with the fresh
// sandbox verbs (DEP-4916) — so for now this returns a clear not-wired error.
// The verb flow above is implemented against snapshotClient and will be
// connected when that client is available. It must use the Sandbox API
// (depot.sandbox.v1), never Depot CI.
func newSnapshotClient(_ context.Context, _ string, _ string) (snapshotClient, error) {
	return nil, fmt.Errorf("`depot sandbox snapshot` is not yet wired: it requires the depot.sandbox.v1 SDK client (DEP-4916, lands with the fresh sandbox verbs). The verb flow is implemented against the Sandbox-API interface and will be connected when that client is available")
}
