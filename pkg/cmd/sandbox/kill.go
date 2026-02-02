package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
)

type killOptions struct {
	token  string
	orgID  string
	stdout io.Writer
	stderr io.Writer
}

// NewCmdKill creates the sandbox kill subcommand
func NewCmdKill() *cobra.Command {
	opts := &killOptions{
		stdout: os.Stdout,
		stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "kill <sandbox-id>",
		Short: "Terminate a sandbox",
		Long: `Terminate a running sandbox.

This immediately stops the sandbox and releases its resources.`,
		Example: `  # Kill a sandbox
  depot sandbox kill sandbox-abc123`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKill(cmd.Context(), args[0], opts)
		},
	}

	cmd.Flags().StringVar(&opts.token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&opts.orgID, "org", "", "Organization ID")

	return cmd
}

func runKill(ctx context.Context, sandboxID string, opts *killOptions) error {
	token, err := helpers.ResolveOrgAuth(ctx, opts.token)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}

	// Check environment variable first, then config file
	if opts.orgID == "" {
		opts.orgID = os.Getenv("DEPOT_ORG_ID")
	}
	if opts.orgID == "" {
		opts.orgID = config.GetCurrentOrganization()
	}

	sandboxClient := api.NewSandboxClient()

	req := &agentv1.KillSandboxRequest{
		SandboxId: sandboxID,
	}

	_, err = sandboxClient.KillSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.orgID))
	if err != nil {
		return fmt.Errorf("unable to kill sandbox: %w", err)
	}

	fmt.Fprintf(opts.stdout, "Sandbox %s has been terminated.\n", sandboxID)
	return nil
}
