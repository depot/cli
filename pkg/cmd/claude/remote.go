package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
)

type AgentRemoteOptions struct {
	SessionID       string
	OrgID           string
	Token           string
	ClaudeArgs      []string
	Repository      string
	Branch          string
	GitSecret       string
	ResumeSessionID string
	RemoteSessionID string
	Wait            bool
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	AgentType       string
}

func RunAgentRemote(ctx context.Context, opts *AgentRemoteOptions) error {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	token, err := helpers.ResolveToken(ctx, opts.Token)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}

	if opts.OrgID == "" {
		opts.OrgID = os.Getenv("DEPOT_ORG_ID")
	}

	client := api.NewAgentClient()

	if opts.RemoteSessionID != "" {
		if opts.SessionID == "" {
			return fmt.Errorf("--session-id is required when using --sandbox-id")
		}

		// Check if the session is still running
		getReq := &agentv1.GetRemoteAgentSessionRequest{
			SessionId:       opts.SessionID,
			RemoteSessionId: opts.RemoteSessionID,
			OrganizationId:  opts.OrgID,
		}
		getResp, err := client.GetRemoteSession(ctx, api.WithAuthentication(connect.NewRequest(getReq), token))
		if err != nil {
			return fmt.Errorf("unable to check remote session status: %w", err)
		}

		if getResp.Msg.CompletedAt == nil {
			if !opts.Wait {
				fmt.Fprintf(opts.Stdout, "\n✓ Claude sandbox is already running!\n")
				fmt.Fprintf(opts.Stdout, "Session ID: %s\n", opts.SessionID)
				fmt.Fprintf(opts.Stdout, "Sandbox ID: %s\n", opts.RemoteSessionID)
				fmt.Fprintf(opts.Stdout, "\nTo wait for this session to complete, run:\n")
				fmt.Fprintf(opts.Stdout, "  depot claude --wait --session-id %s --sandbox-id %s\n", opts.SessionID, opts.RemoteSessionID)
				return nil
			}

			fmt.Fprintf(opts.Stderr, "Claude sandbox %s is already running, waiting for it to complete...\n", opts.RemoteSessionID)
			fmt.Fprintf(opts.Stderr, "\nYou can view this session:\n")
			fmt.Fprintf(opts.Stderr, "- Online: https://depot.dev/orgs/%s/claude/%s\n", opts.OrgID, opts.SessionID)
			fmt.Fprintf(opts.Stderr, "- Locally: depot claude --local --resume %s\n", opts.SessionID)

			// Since the session is already running, skip the wait phase and go directly to streaming
			return streamSession(ctx, client, token, opts.SessionID, opts.RemoteSessionID, opts.OrgID, opts.Stdout, opts.Stderr)
		} else {
			fmt.Fprintf(opts.Stdout, "Sandbox %s has already completed\n", opts.RemoteSessionID)
			if getResp.Msg.ExitCode != nil {
				fmt.Fprintf(opts.Stdout, "Exit code: %d\n", *getResp.Msg.ExitCode)
			}
			fmt.Fprintf(opts.Stdout, "\nYou can view this session:\n")
			fmt.Fprintf(opts.Stdout, "- Online: https://depot.dev/orgs/%s/claude/%s\n", opts.OrgID, opts.SessionID)
			fmt.Fprintf(opts.Stdout, "- Locally: depot claude --local --resume %s\n", opts.SessionID)
			return nil
		}
	}

	if opts.ResumeSessionID != "" {
		return fmt.Errorf("resume by session ID is not supported for remote sessions. Use both --session-id and --sandbox-id to resume")
	}

	req := &agentv1.StartRemoteAgentSessionRequest{
		Argv:                 shellEscapeArgs(opts.ClaudeArgs),
		EnvironmentVariables: map[string]string{},
		AgentType:            &opts.AgentType,
	}
	if opts.OrgID != "" {
		req.OrganizationId = &opts.OrgID
	}
	if opts.SessionID != "" {
		req.SessionId = &opts.SessionID
	}
	if opts.ResumeSessionID != "" {
		req.ResumeSessionId = &opts.ResumeSessionID
	}
	if opts.Repository != "" && isGitURL(opts.Repository) {
		gitURL, gitBranch := parseGitURL(opts.Repository)
		// Use explicit branch if provided, otherwise use branch from URL or default
		if opts.Branch != "" {
			gitBranch = opts.Branch
		}
		gitContext := &agentv1.StartRemoteAgentSessionRequest_Context_GitContext{
			RepositoryUrl: gitURL,
			Branch:        &gitBranch,
		}
		if opts.GitSecret != "" {
			gitContext.SecretName = &opts.GitSecret
		}
		req.Context = &agentv1.StartRemoteAgentSessionRequest_Context{
			Context: &agentv1.StartRemoteAgentSessionRequest_Context_Git{
				Git: gitContext,
			},
		}
	}

	invocationTime := time.Now()
	res, err := client.StartRemoteSession(ctx, api.WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return fmt.Errorf("unable to start Claude sandbox: %w", err)
	}

	sessionID := res.Msg.SessionId
	remoteSessionID := res.Msg.RemoteSessionId

	// If not waiting, just print the URL and exit
	if !opts.Wait {
		fmt.Fprintf(opts.Stdout, "\n✓ Claude sandbox started!\n")
		fmt.Fprintf(opts.Stdout, "Session ID: %s\n", sessionID)
		fmt.Fprintf(opts.Stdout, "Sandbox ID: %s\n", remoteSessionID)
		fmt.Fprintf(opts.Stdout, "\nTo view the Claude session, visit: https://depot.dev/orgs/%s/claude/%s\n", opts.OrgID, sessionID)
		fmt.Fprintf(opts.Stdout, "\nTo wait for this session to complete, run:\n")
		fmt.Fprintf(opts.Stdout, "  depot claude --wait --session-id %s --sandbox-id %s\n", sessionID, remoteSessionID)
		return nil
	}

	return waitAndStreamSession(ctx, client, token, sessionID, remoteSessionID, opts.OrgID, invocationTime, opts.Stdout, opts.Stderr)
}

func isGitURL(s string) bool {
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "ssh://") ||
		strings.Contains(s, ".git")
}

func parseGitURL(s string) (url, branch string) {
	parts := strings.SplitN(s, "#", 2)
	url = parts[0]
	if len(parts) > 1 {
		branch = parts[1]
	} else {
		branch = "main"
	}
	return url, branch
}

func waitForSession(ctx context.Context, client agentv1connect.AgentServiceClient, token, sessionID, remoteSessionID, orgID string, invocationTime time.Time, stdout io.Writer) error {
	fmt.Fprintf(stdout, "\nStarting Claude sandbox for session id %s...\n", sessionID)
	fmt.Fprintf(stdout, "\nYou can view this session:\n")
	fmt.Fprintf(stdout, "- Online: https://depot.dev/orgs/%s/claude/%s\n", orgID, sessionID)
	fmt.Fprintf(stdout, "- Locally: depot claude --local --resume %s\n", sessionID)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(stdout, "\n\nStarting Claude sandbox for session id %s is taking longer than expected to initialize.\n", sessionID)
			fmt.Fprintf(stdout, "The Claude sandbox will continue running in the background.\n")
			return nil
		case <-ticker.C:
			getReq := &agentv1.GetRemoteAgentSessionRequest{
				SessionId:       sessionID,
				RemoteSessionId: remoteSessionID,
				OrganizationId:  orgID,
			}
			getResp, err := client.GetRemoteSession(ctx, api.WithAuthentication(connect.NewRequest(getReq), token))
			if err != nil {
				var connectErr *connect.Error
				if errors.As(err, &connectErr) && connectErr.Code() == connect.CodeNotFound {
					continue // Continue waiting
				}
				return fmt.Errorf("failed to get Claude sandbox status: %w", err)
			}

			if getResp.Msg.StartedAt == nil {
				continue
			}
			sessionStartTime := getResp.Msg.StartedAt.AsTime()
			// Skip if this session started before our invocation
			if !sessionStartTime.After(invocationTime) {
				continue
			}

			fmt.Fprintf(stdout, "\n✓ Claude sandbox started!\n")
			fmt.Fprintf(stdout, "Session ID: %s\n", sessionID)
			fmt.Fprintf(stdout, "Sandbox ID: %s\n", remoteSessionID)
			return nil
		}
	}
}

func streamSession(ctx context.Context, client agentv1connect.AgentServiceClient, token, sessionID, remoteSessionID, orgID string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "==================== REMOTE CLAUDE SESSION ====================\n")

	// Start streaming logs
	streamReq := &agentv1.StreamRemoteAgentSessionLogsRequest{
		RemoteSessionId: remoteSessionID,
		OrganizationId:  &orgID,
	}

	stream, err := client.StreamRemoteSessionLogs(ctx, api.WithAuthentication(connect.NewRequest(streamReq), token))
	if err != nil {
		return fmt.Errorf("failed to stream Claude sandbox logs: %w", err)
	}
	defer stream.Close()

	// Read from the stream
	for stream.Receive() {
		msg := stream.Msg()
		if msg.Event != nil {
			// Write to appropriate output based on log type
			switch msg.Event.Type {
			case agentv1.StreamRemoteAgentSessionLogsResponse_LogEvent_STDERR:
				stderr.Write(msg.Event.Data)
			default: // STDOUT or unspecified
				stdout.Write(msg.Event.Data)
			}
		}
	}

	if err := stream.Err(); err != nil {
		return fmt.Errorf("error streaming logs: %w", err)
	}

	fmt.Fprintf(stdout, "\n==================== END REMOTE CLAUDE SESSION ====================\n")

	getReq := &agentv1.GetRemoteAgentSessionRequest{
		SessionId:       sessionID,
		RemoteSessionId: remoteSessionID,
		OrganizationId:  orgID,
	}
	getResp, err := client.GetRemoteSession(ctx, api.WithAuthentication(connect.NewRequest(getReq), token))
	if err != nil {
		return fmt.Errorf("failed to get final Claude sandbox status: %w", err)
	}

	if getResp.Msg.CompletedAt != nil {
		if getResp.Msg.ExitCode != nil && *getResp.Msg.ExitCode != 0 {
			return fmt.Errorf("Claude sandbox exited with code %d", *getResp.Msg.ExitCode)
		}
	}

	return nil
}

func waitAndStreamSession(ctx context.Context, client agentv1connect.AgentServiceClient, token, sessionID, remoteSessionID, orgID string, invocationTime time.Time, stdout, stderr io.Writer) error {
	// Wait for session to start with a timeout
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	if err := waitForSession(waitCtx, client, token, sessionID, remoteSessionID, orgID, invocationTime, stdout); err != nil {
		return err
	}

	return streamSession(ctx, client, token, sessionID, remoteSessionID, orgID, stdout, stderr)
}

// shellEscapeArgs properly escapes shell arguments to be passed via command line
func shellEscapeArgs(args []string) string {
	escaped := make([]string, 0, len(args))
	for _, arg := range args {
		escaped = append(escaped, shellEscapeArg(arg))
	}
	return strings.Join(escaped, " ")
}

// shellEscapeArg escapes a single shell argument
func shellEscapeArg(s string) string {
	if s == "" {
		return "''"
	}

	// If the string contains no special characters, return as-is
	if !strings.ContainsAny(s, " \t\n\r'\"\\$`!*?#&;|<>(){}[]~") {
		return s
	}

	// Use single quotes and escape any single quotes within
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
