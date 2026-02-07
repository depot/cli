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

type resumeOptions struct {
	timeout   int
	noConnect bool
	token     string
	orgID     string
	debug     bool
	stdout    io.Writer
	stderr    io.Writer
}

// NewCmdResume creates the sandbox resume subcommand
func NewCmdResume() *cobra.Command {
	opts := &resumeOptions{
		timeout: 60,
		stdout:  os.Stdout,
		stderr:  os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Resume a completed sandbox",
		Long: `Resume a completed Depot sandbox environment.

This command restarts a sandbox that has completed, preserving its filesystem
state and creating a new SSH session. Use this to get back into a sandbox
that has timed out or been terminated.`,
		Example: `  # Resume a completed sandbox and connect via SSH
  depot sandbox resume abc123-session-id

  # Resume with custom timeout
  depot sandbox resume abc123-session-id --timeout 90

  # Print connection info only, don't auto-connect
  depot sandbox resume abc123-session-id --no-connect`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResume(cmd.Context(), args[0], opts)
		},
	}

	cmd.Flags().IntVar(&opts.timeout, "timeout", 60, "SSH session timeout in minutes (max 120)")
	cmd.Flags().BoolVar(&opts.noConnect, "no-connect", false, "Print connection info only, don't auto-connect via SSH")
	cmd.Flags().StringVar(&opts.token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&opts.orgID, "org", "", "Organization ID")
	cmd.Flags().BoolVar(&opts.debug, "debug", false, "Enable debug logging")

	return cmd
}

func runResume(ctx context.Context, sessionID string, opts *resumeOptions) error {
	debug := func(format string, args ...any) {
		if opts.debug {
			fmt.Fprintf(opts.stderr, "[DEBUG] "+format+"\n", args...)
		}
	}

	if opts.timeout > 120 {
		return fmt.Errorf("--timeout cannot exceed 120 minutes")
	}

	debug("Resolving authentication token...")
	token, err := helpers.ResolveOrgAuth(ctx, opts.token)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}
	debug("Token resolved (length: %d)", len(token))

	// Check environment variable first, then config file
	if opts.orgID == "" {
		opts.orgID = os.Getenv("DEPOT_ORG_ID")
		debug("Org ID from env: %q", opts.orgID)
	}
	if opts.orgID == "" {
		opts.orgID = config.GetCurrentOrganization()
		debug("Org ID from config: %q", opts.orgID)
	}
	debug("Using org ID: %q", opts.orgID)

	debug("Creating sandbox client...")
	sandboxClient := api.NewSandboxClient()

	// Build the request with resume_session_id
	agentType := agentv1.AgentType_AGENT_TYPE_CLAUDE_CODE
	timeoutMinutes := int32(opts.timeout)
	req := &agentv1.StartSandboxRequest{
		ResumeSessionId:      &sessionID,
		Argv:                 "",
		EnvironmentVariables: map[string]string{},
		AgentType:            agentType,
		SshConfig: &agentv1.SSHConfig{
			Enabled:        true,
			TimeoutMinutes: &timeoutMinutes,
		},
	}
	debug("Request built: ResumeSessionId=%s, AgentType=%v, SSHConfig.Enabled=%v, TimeoutMinutes=%d",
		sessionID, agentType, true, timeoutMinutes)

	// Start spinner for the loading phase
	spin := newSpinner("Resuming sandbox...", opts.stderr)
	if !opts.debug {
		spin.Start()
	}

	debug("Calling StartSandbox API with resume_session_id...")
	res, err := sandboxClient.StartSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.orgID))
	if err != nil {
		spin.Stop()
		debug("StartSandbox API error: %v", err)
		return fmt.Errorf("unable to resume sandbox: %w", err)
	}
	debug("StartSandbox API returned successfully")

	newSessionID := res.Msg.SessionId
	sandboxID := res.Msg.SandboxId
	debug("New Session ID: %s", newSessionID)
	debug("New Sandbox ID: %s", sandboxID)

	// Get SSH connection info - either from response or by polling
	var conn *ssh.SSHConnectionInfo

	if res.Msg.SshConnection != nil && res.Msg.SshConnection.Host != "" {
		debug("SSHConnection available in StartSandbox response")
		conn = &ssh.SSHConnectionInfo{
			Host:       res.Msg.SshConnection.Host,
			Port:       res.Msg.SshConnection.Port,
			Username:   res.Msg.SshConnection.Username,
			PrivateKey: res.Msg.SshConnection.PrivateKey,
		}
	} else {
		debug("SSHConnection not in response, polling for SSH connection...")
		spin.Update("Waiting for sandbox to be ready...")

		conn, err = waitForSSHConnection(ctx, sandboxClient, token, opts.orgID, newSessionID, sandboxID, opts.debug, opts.stderr)
		if err != nil {
			spin.Stop()
			return err
		}
	}

	spin.Stop()
	debug("SSH Host: %s, Port: %d", conn.Host, conn.Port)

	// Print connection info
	info := &ssh.ConnectionInfo{
		SessionID:      newSessionID,
		Host:           conn.Host,
		Port:           conn.Port,
		Username:       conn.Username,
		TimeoutMinutes: opts.timeout,
		CommandName:    "depot sandbox",
	}

	if opts.noConnect {
		debug("--no-connect specified, printing connection info only")
		ssh.PrintConnectionInfo(info, opts.stdout)
		return nil
	}

	// Auto-connect via SSH
	debug("Auto-connecting via SSH...")
	ssh.PrintConnecting(conn, newSessionID, "depot sandbox", opts.stdout)
	debug("Executing SSH command...")
	return ssh.ExecSSH(conn)
}
