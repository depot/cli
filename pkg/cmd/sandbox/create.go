package sandbox

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
	"github.com/spf13/cobra"
)

// newSandboxCreate builds the `create` command, which creates a new sandbox.
// Both the name and the image ref are optional; the server fills in defaults
// when they are omitted.
//
// For a declarative flow driven by a sandbox.depot.yml file, use
// `depot sandbox from-spec`.
func newSandboxCreate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create [flags]",
		Short: "Create a new sandbox",
		Long: `Create a new sandbox via depot.sandbox.v1.CreateSandbox.

Names are optional but recommended for discoverability in 'depot sandbox list'.
The runtime image defaults to the server-side default when --image is omitted.`,
		Example: `
  # Minimal — defaults everywhere
  depot sandbox create

  # Name + custom image
  depot sandbox create --name dev --image ghcr.io/myorg/dev:latest
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token, orgID, err := resolveAuthAndOrg(ctx, cmd)
			if err != nil {
				return err
			}

			name, _ := cmd.Flags().GetString("name")
			image, _ := cmd.Flags().GetString("image")

			req := &sandboxv1.CreateSandboxRequest{}
			if name != "" {
				req.Name = &name
			}
			if image != "" {
				req.Runtime = &sandboxv1.Runtime{
					Runtime: &sandboxv1.Runtime_ImageRef{ImageRef: image},
				}
			}

			client := api.NewSandboxV0Client()
			res, err := client.CreateSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
			if err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}

			sb := res.Msg.Sandbox
			if sb == nil {
				return fmt.Errorf("create sandbox: server returned no sandbox")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Sandbox %s created (org %s, status %s)\n",
				sb.SandboxId, sb.OrganizationId, sb.Status.String())
			fmt.Fprintf(cmd.OutOrStdout(), "Shell: depot sandbox shell %s\n", sb.SandboxId)
			fmt.Fprintf(cmd.OutOrStdout(), "Kill:  depot sandbox kill %s\n", sb.SandboxId)
			return nil
		},
	}

	cmd.Flags().String("name", "", "Human label for the sandbox (org-scoped)")
	cmd.Flags().String("image", "", "OCI image ref for the sandbox runtime (default: server-side default)")
	return cmd
}
