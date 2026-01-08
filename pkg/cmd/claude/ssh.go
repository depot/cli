package claude

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
)

// SSHOptions contains configuration for SSH sandbox mode
type SSHOptions struct {
	OrgID          string
	Token          string
	Repository     string
	Branch         string
	GitSecret      string
	TimeoutMinutes int
	ReconnectID    string
	AutoConnect    bool
	Stdout         io.Writer
	Stderr         io.Writer
}

// RunSSHMode handles SSH sandbox mode - either starting a new SSH session or reconnecting
func RunSSHMode(ctx context.Context, opts *SSHOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	token, err := helpers.ResolveOrgAuth(ctx, opts.Token)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}

	if opts.OrgID == "" {
		opts.OrgID = os.Getenv("DEPOT_ORG_ID")
	}

	sandboxClient := api.NewSandboxClient()

	var tmateSSHURL, tmateWebURL, sessionID string

	if opts.ReconnectID != "" {
		// Reconnect flow - get existing SSH connection
		tmateSSHURL, tmateWebURL, sessionID, err = reconnectSSH(ctx, sandboxClient, token, opts)
		if err != nil {
			return err
		}
	} else {
		// New session flow - start sandbox with SSH enabled
		tmateSSHURL, tmateWebURL, sessionID, err = startSSHSandbox(ctx, sandboxClient, token, opts)
		if err != nil {
			return err
		}
	}

	// Print connection info
	fmt.Fprintf(opts.Stdout, "\nSSH sandbox ready!\n")
	fmt.Fprintf(opts.Stdout, "Session ID: %s\n", sessionID)
	if opts.ReconnectID == "" {
		fmt.Fprintf(opts.Stdout, "Timeout: %d minutes\n", opts.TimeoutMinutes)
	}
	fmt.Fprintf(opts.Stdout, "\n")

	if !opts.AutoConnect {
		// Print URLs only mode
		fmt.Fprintf(opts.Stdout, "Connect via SSH:\n  %s\n", tmateSSHURL)
		if tmateWebURL != "" {
			fmt.Fprintf(opts.Stdout, "\nOr via web browser:\n  %s\n", tmateWebURL)
		}
		fmt.Fprintf(opts.Stdout, "\nTo reconnect later:\n  depot claude --ssh-reconnect %s\n", sessionID)
		return nil
	}

	// Auto-connect via SSH
	fmt.Fprintf(opts.Stdout, "Connecting...\n")
	fmt.Fprintf(opts.Stdout, "SSH URL: %s\n\n", tmateSSHURL)

	fmt.Fprintf(opts.Stdout, "Tip: Your session runs in tmate. To reconnect later, run:\n")
	fmt.Fprintf(opts.Stdout, "  depot claude --ssh-reconnect %s\n\n", sessionID)

	return execTmateSSH(tmateSSHURL)
}

// startSSHSandbox starts a new sandbox with SSH enabled
func startSSHSandbox(ctx context.Context, client agentv1connect.SandboxServiceClient, token string, opts *SSHOptions) (sshURL, webURL, sessionID string, err error) {
	agentType := agentv1.AgentType_AGENT_TYPE_CLAUDE_CODE
	req := &agentv1.StartSandboxRequest{
		Argv:                 "",
		EnvironmentVariables: map[string]string{},
		AgentType:            agentType,
	}

	// Add SSH config when proto types are available
	// For now, this will need to be updated once the API supports SSH mode
	// req.SshConfig = &agentv1.SshConfig{
	//     Enabled:        true,
	//     TimeoutMinutes: int32(opts.TimeoutMinutes),
	// }

	// Add git context if repository is provided
	if opts.Repository != "" && isGitURL(opts.Repository) {
		gitURL, gitBranch := parseGitURL(opts.Repository)
		if opts.Branch != "" {
			gitBranch = opts.Branch
		}
		gitContext := &agentv1.StartSandboxRequest_Context_GitContext{
			RepositoryUrl: gitURL,
			Branch:        &gitBranch,
		}
		if opts.GitSecret != "" {
			gitContext.SecretName = &opts.GitSecret
		}
		req.Context = &agentv1.StartSandboxRequest_Context{
			Context: &agentv1.StartSandboxRequest_Context_Git{
				Git: gitContext,
			},
		}
	}

	res, err := client.StartSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.OrgID))
	if err != nil {
		return "", "", "", fmt.Errorf("unable to start SSH sandbox: %w", err)
	}

	sessionID = res.Msg.SessionId

	// Check for tmate connection info in response
	// This will be available once the API supports SSH mode
	// if res.Msg.TmateConnection != nil {
	//     sshURL = res.Msg.TmateConnection.SshUrl
	//     webURL = res.Msg.TmateConnection.WebUrl
	// }

	// For now, return an error indicating SSH mode is not yet available from the API
	// Include the sessionID in the error message so the assignment is used
	return "", "", sessionID, fmt.Errorf("SSH mode is not yet available. The API does not support SSH sandbox mode (session %s was created but tmate connection info is not available). Please check for updates to the depot CLI", sessionID)
}

// reconnectSSH retrieves connection info for an existing SSH sandbox
func reconnectSSH(ctx context.Context, client agentv1connect.SandboxServiceClient, token string, opts *SSHOptions) (sshURL, webURL, sessionID string, err error) {
	// Get SSH connection info for existing sandbox
	// This will use GetSSHConnection API once proto types are available
	// req := &agentv1.GetSSHConnectionRequest{
	//     SessionId: opts.ReconnectID,
	// }
	// res, err := client.GetSSHConnection(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.OrgID))

	// For now, return an error indicating this is not yet available
	return "", "", "", fmt.Errorf("SSH reconnect is not yet available. The API does not support GetSSHConnection. Please check for updates to the depot CLI")
}

// parseTmateSSHURL parses a tmate SSH URL and returns the SSH arguments.
// The URL is expected to be in the format "ssh XXX@host" where XXX is a session token.
func parseTmateSSHURL(tmateSSHURL string) ([]string, error) {
	// Parse "ssh XXX@host" format
	parts := strings.Fields(tmateSSHURL)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid tmate SSH URL format: %s", tmateSSHURL)
	}

	// Extract user@host from the URL
	userHost := parts[1]

	// Build SSH command with appropriate options
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ServerAliveInterval=30",
		userHost,
	}

	return sshArgs, nil
}

// execTmateSSH connects to a tmate session via SSH
func execTmateSSH(tmateSSHURL string) error {
	sshArgs, err := parseTmateSSHURL(tmateSSHURL)
	if err != nil {
		return err
	}

	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
