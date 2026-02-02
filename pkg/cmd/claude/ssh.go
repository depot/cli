package claude

import (
	"context"
	"fmt"
	"io"
	"os"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/depot/cli/pkg/ssh"
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
	timeoutMinutes := int32(opts.TimeoutMinutes)
	req := &agentv1.StartSandboxRequest{
		Argv:                 "",
		EnvironmentVariables: map[string]string{},
		AgentType:            agentType,
		SshConfig: &agentv1.SSHConfig{
			Enabled:        true,
			TimeoutMinutes: &timeoutMinutes,
		},
	}

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
	if res.Msg.TmateConnection == nil {
		return "", "", sessionID, fmt.Errorf("SSH sandbox started but tmate connection info is not available. The sandbox may not support SSH mode yet")
	}

	sshURL = res.Msg.TmateConnection.SshUrl
	if res.Msg.TmateConnection.WebUrl != nil {
		webURL = *res.Msg.TmateConnection.WebUrl
	}

	return sshURL, webURL, sessionID, nil
}

// reconnectSSH retrieves connection info for an existing SSH sandbox
func reconnectSSH(ctx context.Context, client agentv1connect.SandboxServiceClient, token string, opts *SSHOptions) (sshURL, webURL, sessionID string, err error) {
	req := &agentv1.GetSSHConnectionRequest{
		SessionId: opts.ReconnectID,
	}

	res, err := client.GetSSHConnection(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.OrgID))
	if err != nil {
		return "", "", "", fmt.Errorf("unable to get SSH connection info: %w", err)
	}

	if res.Msg.TmateConnection == nil {
		return "", "", "", fmt.Errorf("no SSH connection available for session %s", opts.ReconnectID)
	}

	sshURL = res.Msg.TmateConnection.SshUrl
	if res.Msg.TmateConnection.WebUrl != nil {
		webURL = *res.Msg.TmateConnection.WebUrl
	}

	return sshURL, webURL, opts.ReconnectID, nil
}

// execTmateSSH connects to a tmate session via SSH using the shared SSH package
func execTmateSSH(tmateSSHURL string) error {
	return ssh.ExecSSH(tmateSSHURL)
}
