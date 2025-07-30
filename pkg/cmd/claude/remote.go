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

type ClaudeRemoteOptions struct {
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

func RunClaudeRemote(ctx context.Context, opts *ClaudeRemoteOptions) error {
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

	client := api.NewClaudeClient()

	if opts.ResumeSessionID != "" {
		getReq := &agentv1.GetRemoteSessionRequest{
			SessionId:      opts.ResumeSessionID,
			OrganizationId: opts.OrgID,
		}
		getResp, err := client.GetRemoteSession(ctx, api.WithAuthentication(connect.NewRequest(getReq), token))
		if err != nil {
			fmt.Fprintf(opts.Stderr, "Warning: unable to check Claude sandbox status: %v\n", err)
		} else if getResp.Msg.CompletedAt == nil {
			if !opts.Wait {
				fmt.Fprintf(opts.Stdout, "\n✓ Claude sandbox is already running!\n")
				fmt.Fprintf(opts.Stdout, "Session ID: %s\n", opts.ResumeSessionID)
				fmt.Fprintf(opts.Stdout, "\nTo view the Claude sandbox session, visit: https://depot.dev/orgs/%s/claude/%s\n", opts.OrgID, opts.ResumeSessionID)
				fmt.Fprintf(opts.Stdout, "\nTo wait for this session to complete, run:\n")
				fmt.Fprintf(opts.Stdout, "  depot claude --wait --resume %s\n", opts.ResumeSessionID)
				return nil
			}

			fmt.Fprintf(opts.Stderr, "Claude sandbox %s is already running, waiting for it to complete...\n", opts.ResumeSessionID)

			// Since we don't know exactly when it started, use zero time
			invocationTime := time.Time{}
			return waitAndStreamSession(ctx, client, token, opts.ResumeSessionID, opts.OrgID, invocationTime, opts.Stdout, opts.Stderr)
		}
	}

	req := &agentv1.StartRemoteSessionRequest{
		Argv:                 shellEscapeArgs(opts.ClaudeArgs),
		EnvironmentVariables: map[string]string{},
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
		gitContext := &agentv1.StartRemoteSessionRequest_Context_GitContext{
			RepositoryUrl: gitURL,
			Branch:        &gitBranch,
		}
		if opts.GitSecret != "" {
			gitContext.SecretName = &opts.GitSecret
		}
		req.Context = &agentv1.StartRemoteSessionRequest_Context{
			Context: &agentv1.StartRemoteSessionRequest_Context_Git{
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

	// If not waiting, just print the URL and exit
	if !opts.Wait {
		fmt.Fprintf(opts.Stdout, "\n✓ Claude sandbox started!\n")
		fmt.Fprintf(opts.Stdout, "Session ID: %s\n", sessionID)
		fmt.Fprintf(opts.Stdout, "\nTo view the Claude sandbox session, visit: https://depot.dev/orgs/%s/claude/%s\n", opts.OrgID, sessionID)
		fmt.Fprintf(opts.Stdout, "\nTo wait for this session to complete, run:\n")
		fmt.Fprintf(opts.Stdout, "  depot claude --wait --resume %s\n", sessionID)
		return nil
	}

	return waitAndStreamSession(ctx, client, token, sessionID, opts.OrgID, invocationTime, opts.Stdout, opts.Stderr)
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

func waitForSession(ctx context.Context, client agentv1connect.ClaudeServiceClient, token, sessionID, orgID string, invocationTime time.Time, stdout io.Writer) error {
	fmt.Fprintf(stdout, "\nStarting Claude sandbox for session id %s...\n", sessionID)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(stdout, "\n\nStarting Claude sandbox for session id %s is taking longer than expected to initialize.\n", sessionID)
			fmt.Fprintf(stdout, "The Claude sandbox will continue running in the background.\n")
			return nil
		case <-ticker.C:
			getReq := &agentv1.GetRemoteSessionRequest{
				SessionId:      sessionID,
				OrganizationId: orgID,
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
			return nil
		}
	}
}

func streamSession(ctx context.Context, client agentv1connect.ClaudeServiceClient, token, sessionID, orgID string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "Claude sandbox is running. Session ID: %s\n", sessionID)
	fmt.Fprintf(stdout, "You can resume this Claude sandbox later with: depot claude --resume %s\n\n", sessionID)

	// Poll for session status until it completes
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			getReq := &agentv1.GetRemoteSessionRequest{
				SessionId:      sessionID,
				OrganizationId: orgID,
			}
			getResp, err := client.GetRemoteSession(ctx, api.WithAuthentication(connect.NewRequest(getReq), token))
			if err != nil {
				return fmt.Errorf("failed to get Claude sandbox status: %w", err)
			}

			if getResp.Msg.CompletedAt != nil {
				if getResp.Msg.ExitCode != nil && *getResp.Msg.ExitCode != 0 {
					fmt.Fprintf(stdout, "\n✗ Claude sandbox exited with code %d\n", *getResp.Msg.ExitCode)
					return fmt.Errorf("Claude sandbox exited with code %d", *getResp.Msg.ExitCode)
				} else {
					fmt.Fprintf(stdout, "\n✓ Claude sandbox exited successfully (exit code 0)\n")
					return nil
				}
			}

			// Print status update
			fmt.Fprintf(stdout, ".")
		}
	}
}

func waitAndStreamSession(ctx context.Context, client agentv1connect.ClaudeServiceClient, token, sessionID, orgID string, invocationTime time.Time, stdout, stderr io.Writer) error {
	// Wait for session to start with a timeout
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	if err := waitForSession(waitCtx, client, token, sessionID, orgID, invocationTime, stdout); err != nil {
		return err
	}

	return streamSession(ctx, client, token, sessionID, orgID, stdout, stderr)
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
