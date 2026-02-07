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

type execOptions struct {
	token  string
	orgID  string
	stdout io.Writer
	stderr io.Writer
}

// NewCmdExec creates the sandbox exec subcommand
func NewCmdExec() *cobra.Command {
	opts := &execOptions{
		stdout: os.Stdout,
		stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "exec <session-id> <command> [args...]",
		Short: "Execute a command in a running sandbox",
		Long: `Execute a command in a running SSH sandbox session.

This command connects to an existing sandbox via SSH and executes
the specified command non-interactively, streaming the output back
to your terminal.`,
		Example: `  # List files in a sandbox
  depot sandbox exec abc123-session-id ls -la

  # Run a script in a sandbox
  depot sandbox exec abc123-session-id ./build.sh

  # Check git status
  depot sandbox exec abc123-session-id git status`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
			command := args[1:]
			return runExec(cmd.Context(), sessionID, command, opts)
		},
	}

	cmd.Flags().StringVar(&opts.token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&opts.orgID, "org", "", "Organization ID")

	return cmd
}

func runExec(ctx context.Context, sessionID string, command []string, opts *execOptions) error {
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

	if res.Msg.SshConnection == nil {
		return fmt.Errorf("no SSH connection available for session %s", sessionID)
	}

	conn := &ssh.SSHConnectionInfo{
		Host:       res.Msg.SshConnection.Host,
		Port:       res.Msg.SshConnection.Port,
		Username:   res.Msg.SshConnection.Username,
		PrivateKey: res.Msg.SshConnection.PrivateKey,
	}

	// Execute the command via SSH
	exitCode, err := ssh.ExecSSHCommand(conn, command, os.Stdin, opts.stdout, opts.stderr)
	if err != nil {
		return fmt.Errorf("failed to execute command: %w", err)
	}

	if exitCode != 0 {
		os.Exit(exitCode)
	}

	return nil
}
