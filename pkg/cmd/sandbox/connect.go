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
	"github.com/depot/cli/pkg/ssh"
	"github.com/spf13/cobra"
)

type connectOptions struct {
	token  string
	orgID  string
	stdout io.Writer
	stderr io.Writer
}

// NewCmdConnect creates the sandbox connect subcommand
func NewCmdConnect() *cobra.Command {
	opts := &connectOptions{
		stdout: os.Stdout,
		stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "connect <session-id>",
		Short: "Connect to an existing sandbox via SSH",
		Long: `Connect to an existing SSH sandbox session.

This command retrieves the SSH connection information for an existing
sandbox and connects to it via SSH.`,
		Example: `  # Connect to an existing session
  depot sandbox connect abc123-session-id`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnect(cmd.Context(), args[0], opts)
		},
	}

	cmd.Flags().StringVar(&opts.token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&opts.orgID, "org", "", "Organization ID")

	return cmd
}

func runConnect(ctx context.Context, sessionID string, opts *connectOptions) error {
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

	// Get SSH connection info for existing sandbox
	req := &agentv1.GetSSHConnectionRequest{
		SessionId: sessionID,
	}

	res, err := sandboxClient.GetSSHConnection(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.orgID))
	if err != nil {
		return fmt.Errorf("unable to get SSH connection info: %w", err)
	}

	if res.Msg.TmateConnection == nil {
		return fmt.Errorf("no SSH connection available for session %s", sessionID)
	}

	sshURL := res.Msg.TmateConnection.SshUrl

	// Connect via SSH
	ssh.PrintConnecting(sshURL, sessionID, "depot sandbox", opts.stdout)
	return ssh.ExecSSH(sshURL)
}
