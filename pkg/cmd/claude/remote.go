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
	Wait            bool
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
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

	if opts.ResumeSessionID != "" {
		// For remote sessions, we need to check if there's an existing sandbox for this session
		agentType := agentv1.AgentType_AGENT_TYPE_CLAUDE_CODE
		listReq := &agentv1.ListSandboxsRequest{
			AgentType: &agentType,
		}
		listResp, err := sandboxClient.ListSandboxs(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(listReq), token, opts.OrgID))
		if err != nil {
			return fmt.Errorf("failed to list sandboxes: %w", err)
		}

		var foundSandbox *agentv1.Sandbox
		for _, sandbox := range listResp.Msg.Sandboxes {
			if sandbox.SessionId == opts.ResumeSessionID {
				foundSandbox = sandbox
				break
			}
		}

		if foundSandbox != nil {
			if foundSandbox.CompletedAt == nil {
				if !opts.Wait {
					fmt.Fprintf(opts.Stdout, "\n✓ Claude sandbox is already running for session %s!\n", opts.ResumeSessionID)
					fmt.Fprintf(opts.Stdout, "\nTo wait for this session to complete, run:\n")
					fmt.Fprintf(opts.Stdout, "  depot claude --wait --resume %s\n", opts.ResumeSessionID)
					return nil
				}

				fmt.Fprintf(opts.Stderr, "Claude sandbox for session %s is already running, waiting for it to complete...\n", opts.ResumeSessionID)
				return streamSandboxLogs(ctx, sandboxClient, token, foundSandbox.SandboxId, foundSandbox.SessionId, foundSandbox.OrganizationId, opts.Stdout, opts.Stderr)
			} else {
				if opts.Wait {
					fmt.Fprintf(opts.Stdout, "Session %s has already completed.\n", opts.ResumeSessionID)
					fmt.Fprintf(opts.Stdout, "\nTo resume the session locally, run:\n")
					fmt.Fprintf(opts.Stdout, "  depot claude --local --resume %s\n", opts.ResumeSessionID)
					return nil
				}
			}
		} else if opts.Wait {
			return fmt.Errorf("session '%s' not found or already completed. To resume locally, use: depot claude --local --resume %s", opts.ResumeSessionID, opts.ResumeSessionID)
		}
	}

	agentType := agentv1.AgentType_AGENT_TYPE_CLAUDE_CODE
	req := &agentv1.StartSandboxRequest{
		Argv:                 shellEscapeArgs(opts.ClaudeArgs),
		EnvironmentVariables: map[string]string{},
		AgentType:            agentType,
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

	invocationTime := time.Now()
	res, err := sandboxClient.StartSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.OrgID))
	if err != nil {
		return fmt.Errorf("unable to start Claude sandbox: %w", err)
	}

	sessionID := res.Msg.SessionId
	sandboxID := res.Msg.SandboxId

	// If not waiting, just print the URL and exit
	if !opts.Wait {
		fmt.Fprintf(opts.Stdout, "\n✓ Claude sandbox started!\n")
		fmt.Fprintf(opts.Stdout, "Session ID: %s\n", sessionID)
		fmt.Fprintf(opts.Stdout, "\nTo view the Claude session, visit: https://depot.dev/orgs/%s/claude/%s\n", opts.OrgID, sessionID)
		fmt.Fprintf(opts.Stdout, "\nTo wait for this session to complete, run:\n")
		fmt.Fprintf(opts.Stdout, "  depot claude --wait --resume %s\n", sessionID)
		return nil
	}

	return waitAndStreamSandbox(ctx, sandboxClient, token, sessionID, sandboxID, opts.OrgID, invocationTime, opts.Stdout, opts.Stderr)
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

func waitForSandbox(ctx context.Context, client agentv1connect.SandboxServiceClient, token, sessionID, sandboxID, orgID string, invocationTime time.Time, stdout io.Writer) error {
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
			getReq := &agentv1.GetSandboxRequest{
				SandboxId: sandboxID,
			}
			getResp, err := client.GetSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(getReq), token, orgID))
			if err != nil {
				var connectErr *connect.Error
				if errors.As(err, &connectErr) && connectErr.Code() == connect.CodeNotFound {
					continue // Continue waiting
				}
				return fmt.Errorf("failed to get Claude sandbox status: %w", err)
			}

			sandbox := getResp.Msg.Sandbox
			if sandbox.StartedAt == nil {
				continue
			}
			sessionStartTime := sandbox.StartedAt.AsTime()
			// Skip if this session started before our invocation
			if !sessionStartTime.After(invocationTime) {
				continue
			}

			fmt.Fprintf(stdout, "\n✓ Claude sandbox started!\n")
			fmt.Fprintf(stdout, "Session ID: %s\n", sessionID)
			return nil
		}
	}
}

func streamSandboxLogs(ctx context.Context, client agentv1connect.SandboxServiceClient, token, sandboxID, sessionID, orgID string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "==================== REMOTE CLAUDE SESSION ====================\n")

	// Start streaming logs
	streamReq := &agentv1.StreamSandboxLogsRequest{
		SandboxId: sandboxID,
	}

	stream, err := client.StreamSandboxLogs(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(streamReq), token, orgID))
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
			case agentv1.StreamSandboxLogsResponse_LogEvent_LOG_TYPE_STDERR:
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

	getReq := &agentv1.GetSandboxRequest{
		SandboxId: sandboxID,
	}
	getResp, err := client.GetSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(getReq), token, orgID))
	if err != nil {
		return fmt.Errorf("failed to get final Claude sandbox status: %w", err)
	}

	sandbox := getResp.Msg.Sandbox
	if sandbox.CompletedAt != nil {
		if sandbox.ExitCode != nil && *sandbox.ExitCode != 0 {
			return fmt.Errorf("Claude sandbox exited with code %d", *sandbox.ExitCode)
		}
	}

	return nil
}

func waitAndStreamSandbox(ctx context.Context, client agentv1connect.SandboxServiceClient, token, sessionID, sandboxID, orgID string, invocationTime time.Time, stdout, stderr io.Writer) error {
	// Wait for session to start with a timeout
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	if err := waitForSandbox(waitCtx, client, token, sessionID, sandboxID, orgID, invocationTime, stdout); err != nil {
		return err
	}

	return streamSandboxLogs(ctx, client, token, sandboxID, sessionID, orgID, stdout, stderr)
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
