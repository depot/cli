package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/depot/cli/pkg/ssh"
	"github.com/spf13/cobra"
)

type startOptions struct {
	ssh        bool
	timeout    int
	repository string
	branch     string
	gitSecret  string
	template   string
	image      string
	command    string
	noWait     bool
	token      string
	orgID      string
	debug      bool
	stdout     io.Writer
	stderr     io.Writer
}

// NewCmdStart creates the sandbox start subcommand
func NewCmdStart() *cobra.Command {
	opts := &startOptions{
		timeout: 60,
		stdout:  os.Stdout,
		stderr:  os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a new sandbox",
		Long: `Start a new Depot sandbox environment.

By default, starts a sandbox and prints connection info.
Use --ssh to automatically connect to the sandbox via SSH.

Use --command to execute a command inside the sandbox and print its output.

Use --template to start from a pre-configured template with dependencies
and tools already installed.

Use --image to start a sandbox from a custom Depot Registry image.
The --image flag is mutually exclusive with --ssh and --template.`,
		Example: `  # Start a sandbox and connect via SSH
  depot sandbox start --ssh

  # Start a sandbox and print connection info
  depot sandbox start

  # Start with custom timeout
  depot sandbox start --ssh --timeout 90

  # Start with git repo context
  depot sandbox start --ssh --repository https://github.com/user/repo.git

  # Start from a template
  depot sandbox start --ssh --template my-dev-env

  # Execute a command in a new sandbox and see the output
  depot sandbox start --command "ls -la"

  # Start from a custom Depot Registry image
  depot sandbox start --image registry.depot.dev/org/repo:tag

  # Start from a custom image and run a command
  depot sandbox start --image registry.depot.dev/org/repo:tag --command "make test"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStart(cmd.Context(), opts)
		},
	}

	cmd.Flags().BoolVar(&opts.ssh, "ssh", false, "Connect to the sandbox via SSH after starting")
	cmd.Flags().IntVar(&opts.timeout, "timeout", 60, "SSH session timeout in minutes (max 120)")
	cmd.Flags().StringVar(&opts.repository, "repository", "", "Git repository URL to clone")
	cmd.Flags().StringVar(&opts.branch, "branch", "", "Git branch to checkout")
	cmd.Flags().StringVar(&opts.gitSecret, "git-secret", "", "Secret name for private repo credentials")
	cmd.Flags().StringVar(&opts.template, "template", "", "Template name or ID to start from")
	cmd.Flags().StringVar(&opts.image, "image", "", "Custom Depot Registry image (registry.depot.dev/...)")
	cmd.Flags().StringVar(&opts.command, "command", "", "Command to execute inside the sandbox and print its output")
	cmd.Flags().BoolVar(&opts.noWait, "no-wait", false, "Don't wait for the command to complete (fire and forget)")
	cmd.Flags().StringVar(&opts.token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&opts.orgID, "org", "", "Organization ID")
	cmd.Flags().BoolVar(&opts.debug, "debug", false, "Enable debug logging")

	return cmd
}

func runStart(ctx context.Context, opts *startOptions) error {
	debug := func(format string, args ...interface{}) {
		if opts.debug {
			fmt.Fprintf(opts.stderr, "[DEBUG] "+format+"\n", args...)
		}
	}

	if opts.timeout > 120 {
		return fmt.Errorf("--timeout cannot exceed 120 minutes")
	}

	if opts.ssh && opts.command != "" {
		return fmt.Errorf("--ssh and --command cannot be used together")
	}
	if opts.image != "" && opts.ssh {
		return fmt.Errorf("--image and --ssh cannot be used together")
	}
	if opts.image != "" && opts.template != "" {
		return fmt.Errorf("--image and --template cannot be used together")
	}
	if opts.image != "" && opts.repository != "" {
		return fmt.Errorf("--image and --repository cannot be used together")
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

	// Build the request
	agentType := agentv1.AgentType_AGENT_TYPE_UNSPECIFIED
	timeoutMinutes := int32(opts.timeout)
	req := &agentv1.StartSandboxRequest{
		Argv:                 "",
		EnvironmentVariables: map[string]string{},
		AgentType:            agentType,
	}

	// Check template SSH compatibility before configuring SSH
	templateSSHCompatible := true
	if opts.template != "" {
		req.TemplateId = &opts.template
		debug("Template: %s, checking SSH compatibility...", opts.template)

		tmpl, err := resolveTemplate(ctx, sandboxClient, token, opts.orgID, opts.template)
		if err != nil {
			debug("Could not resolve template SSH compatibility: %v", err)
		} else {
			templateSSHCompatible = tmpl.SshCompatible
			debug("Template SSH compatible: %v", templateSSHCompatible)
		}
	}

	// Configure SSH unless using --image or template doesn't support it
	if opts.image != "" {
		req.Image = &opts.image
	} else if templateSSHCompatible {
		req.SshConfig = &agentv1.SSHConfig{
			Enabled:        true,
			TimeoutMinutes: &timeoutMinutes,
		}
	} else {
		req.SshConfig = &agentv1.SSHConfig{
			Enabled: false,
		}
	}

	// Add startup command if provided
	if opts.command != "" {
		req.Command = &opts.command
		if !opts.noWait {
			waitForCommand := true
			req.WaitForCommand = &waitForCommand
		}
		debug("Startup command: %q (no-wait=%v)", opts.command, opts.noWait)
	}

	if opts.image != "" {
		debug("Request built: AgentType=%v, Image=%s", agentType, opts.image)
	} else if !templateSSHCompatible {
		debug("Request built: AgentType=%v, SSH disabled (template not SSH compatible)", agentType)
	} else {
		debug("Request built: AgentType=%v, SSHConfig.Enabled=%v, TimeoutMinutes=%d", agentType, true, timeoutMinutes)
	}

	// Add git context if repository is explicitly provided
	if opts.repository != "" {
		gitURL, gitBranch := parseGitURL(opts.repository)
		if opts.branch != "" {
			gitBranch = opts.branch
		}
		gitContext := &agentv1.StartSandboxRequest_Context_GitContext{
			RepositoryUrl: gitURL,
			Branch:        &gitBranch,
		}
		if opts.gitSecret != "" {
			gitContext.SecretName = &opts.gitSecret
		}
		req.Context = &agentv1.StartSandboxRequest_Context{
			Context: &agentv1.StartSandboxRequest_Context_Git{
				Git: gitContext,
			},
		}
		debug("Git context added: URL=%s, Branch=%s", gitURL, gitBranch)
	}

	// Start spinner for the loading phase
	spin := newSpinner("Starting sandbox...", opts.stderr)
	if !opts.debug {
		spin.Start()
	}

	debug("Calling StartSandbox API...")
	res, err := sandboxClient.StartSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.orgID))
	if err != nil {
		spin.Stop()
		debug("StartSandbox API error: %v", err)
		return fmt.Errorf("unable to start sandbox: %w", err)
	}
	debug("StartSandbox API returned successfully")

	sessionID := res.Msg.SessionId
	sandboxID := res.Msg.SandboxId
	debug("Session ID: %s", sessionID)
	debug("Sandbox ID: %s", sandboxID)

	// Print command result if wait_for_command was set
	if res.Msg.CommandResult != nil {
		spin.Stop()
		cr := res.Msg.CommandResult
		if cr.Stdout != "" {
			fmt.Fprint(opts.stdout, cr.Stdout)
		}
		if cr.Stderr != "" {
			fmt.Fprint(opts.stderr, cr.Stderr)
		}
		if cr.ExitCode != 0 {
			fmt.Fprintf(opts.stderr, "Command exited with code %d\n", cr.ExitCode)
		}

		// If not connecting via SSH, just print the IDs and exit
		if !opts.ssh {
			fmt.Fprintf(opts.stdout, "\nSandbox ID: %s\n", sandboxID)
			fmt.Fprintf(opts.stdout, "Session ID: %s\n", sessionID)
			return nil
		}
	}

	// For sandboxes without SSH (--image or non-SSH-compatible template), just print IDs and exec hint
	if opts.image != "" || !templateSSHCompatible {
		spin.Stop()
		fmt.Fprintf(opts.stdout, "\nSandbox ready!\n")
		fmt.Fprintf(opts.stdout, "Sandbox ID: %s\n", sandboxID)
		fmt.Fprintf(opts.stdout, "Session ID: %s\n", sessionID)
		fmt.Fprintf(opts.stdout, "\nTo exec:\n  depot sandbox exec %s <command>\n", sandboxID)
		return nil
	}

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

		conn, err = waitForSSHConnection(ctx, sandboxClient, token, opts.orgID, sessionID, sandboxID, opts.debug, opts.stderr)
		if err != nil {
			spin.Stop()
			return err
		}
	}

	spin.Stop()
	debug("SSH Host: %s, Port: %d", conn.Host, conn.Port)

	// If --ssh not passed, print connection info and exit
	if !opts.ssh {
		debug("--ssh not specified, printing connection info only")
		info := &ssh.ConnectionInfo{
			SandboxID:      sandboxID,
			SessionID:      sessionID,
			Host:           conn.Host,
			Port:           conn.Port,
			Username:       conn.Username,
			TimeoutMinutes: opts.timeout,
			CommandName:    "depot sandbox",
		}
		ssh.PrintConnectionInfo(info, opts.stdout)
		return nil
	}

	// Auto-connect via SSH
	debug("Auto-connecting via SSH...")
	ssh.PrintConnecting(conn, sessionID, "depot sandbox", opts.stdout)
	debug("Executing SSH command...")
	return ssh.ExecSSH(conn)
}

// isGitURL checks if a string looks like a git URL
func isGitURL(s string) bool {
	return len(s) > 0 && (s[0:4] == "http" || s[0:3] == "git" || s[0:3] == "ssh" || contains(s, ".git"))
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// parseGitURL extracts the URL and optional branch from a git URL
func parseGitURL(s string) (url, branch string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' {
			return s[:i], s[i+1:]
		}
	}
	return s, "main"
}

// resolveTemplate looks up a template by name or ID and returns it.
func resolveTemplate(ctx context.Context, client agentv1connect.SandboxServiceClient, token, orgID, nameOrID string) (*agentv1.SandboxTemplate, error) {
	req := &agentv1.ListSandboxTemplatesRequest{}
	res, err := client.ListSandboxTemplates(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return nil, fmt.Errorf("failed to list templates: %w", err)
	}
	for _, tmpl := range res.Msg.Templates {
		if tmpl.Name == nameOrID || tmpl.Id == nameOrID {
			return tmpl, nil
		}
	}
	return nil, fmt.Errorf("template %q not found", nameOrID)
}

// waitForSSHConnection polls until the SSH connection is available, then fetches the full
// connection info (including the private key) via GetSSHConnection.
func waitForSSHConnection(ctx context.Context, client agentv1connect.SandboxServiceClient, token, orgID, sessionID, sandboxID string, debugEnabled bool, stderr io.Writer) (*ssh.SSHConnectionInfo, error) {
	debug := func(format string, args ...interface{}) {
		if debugEnabled {
			fmt.Fprintf(stderr, "[DEBUG] "+format+"\n", args...)
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Timeout after 5 minutes
	timeout := time.After(5 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case <-timeout:
			return nil, fmt.Errorf("timed out waiting for SSH connection to become available")

		case <-ticker.C:
			debug("Polling GetSandbox for SSH connection info...")
			getReq := &agentv1.GetSandboxRequest{
				SandboxId: sandboxID,
			}
			getResp, err := client.GetSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(getReq), token, orgID))
			if err != nil {
				var connectErr *connect.Error
				if errors.As(err, &connectErr) && connectErr.Code() == connect.CodeNotFound {
					debug("Sandbox not found yet, continuing to poll...")
					continue
				}
				return nil, fmt.Errorf("failed to get sandbox status: %w", err)
			}

			sandbox := getResp.Msg.Sandbox
			debug("Sandbox state: StartedAt=%v, SshEnabled=%v, SshHost=%v, SshPort=%v",
				sandbox.StartedAt != nil,
				sandbox.SshEnabled != nil && *sandbox.SshEnabled,
				sandbox.SshHost != nil,
				sandbox.SshPort != nil)

			// Check if SSH host/port is available
			if sandbox.SshHost != nil && *sandbox.SshHost != "" {
				debug("SSH host available, fetching full connection info...")

				// Get the private key via GetSSHConnection
				sshReq := &agentv1.GetSSHConnectionRequest{
					SessionId: sessionID,
				}
				sshResp, err := client.GetSSHConnection(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(sshReq), token, orgID))
				if err != nil {
					return nil, fmt.Errorf("failed to get SSH connection details: %w", err)
				}
				if sshResp.Msg.SshConnection == nil {
					return nil, fmt.Errorf("SSH connection info not available")
				}

				debug("SSH connection available!")
				return &ssh.SSHConnectionInfo{
					Host:       sshResp.Msg.SshConnection.Host,
					Port:       sshResp.Msg.SshConnection.Port,
					Username:   sshResp.Msg.SshConnection.Username,
					PrivateKey: sshResp.Msg.SshConnection.PrivateKey,
				}, nil
			}

			debug("SSH connection not ready yet, continuing to poll...")
		}
	}
}
