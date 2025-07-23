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
	RemoteContext   string
	GitSecret       string
	ResumeSessionID string
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

	if err := checkRequiredClaudeSecrets(ctx, client, token, opts.OrgID, opts.Stderr); err != nil {
		fmt.Fprintf(opts.Stderr, "%v\n", err.Error())
	}

	nonInteractiveMode := false
	for _, arg := range opts.ClaudeArgs {
		if arg == "-p" {
			nonInteractiveMode = true
		}
	}

	if !nonInteractiveMode {
		return fmt.Errorf("remote sessions require the -p flag to run in non-interactive mode")
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
	if opts.RemoteContext != "" && isGitURL(opts.RemoteContext) {
		gitURL, gitBranch := parseGitURL(opts.RemoteContext)
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
		return fmt.Errorf("unable to start remote session: %w", err)
	}

	sessionID := res.Msg.SessionId

	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

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

func waitAndStreamSession(ctx context.Context, client agentv1connect.ClaudeServiceClient, token, sessionID, orgID string, invocationTime time.Time, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "\nWaiting for remote session %s to initialize...\n", sessionID)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	sessionStarted := false
	sessionRunning := false

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(stdout, "\n\nRemote session is taking longer than expected to initialize.\n")
			fmt.Fprintf(stdout, "Session ID: %s\n", sessionID)
			fmt.Fprintf(stdout, "The session will continue running in the background.\n")
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
				return fmt.Errorf("failed to get remote session status: %w", err)
			}

			if getResp.Msg.StartedAt == nil {
				continue
			}
			sessionStartTime := getResp.Msg.StartedAt.AsTime()
			if !sessionStartTime.After(invocationTime) {
				continue
			}

			if !sessionStarted {
				sessionStarted = true
				fmt.Fprintf(stdout, "\n✓ Remote session started!\n")
				fmt.Fprintf(stdout, "Session ID: %s\n", sessionID)
			}

			if getResp.Msg.CompletedAt != nil {
				exitCode := 0
				if getResp.Msg.ExitCode != nil {
					exitCode = int(*getResp.Msg.ExitCode)
				}

				if exitCode != 0 {
					fmt.Fprintf(stderr, "\n✗ Remote session exited with code %d\n", exitCode)
					fmt.Fprintf(stderr, "Session ID: %s\n", sessionID)
					fmt.Fprintf(stderr, "View session logs: https://depot.dev/orgs/%s/claude/sessions/%s\n", orgID, sessionID)
					if getResp.Msg.ErrorMessage != nil && *getResp.Msg.ErrorMessage != "" {
						fmt.Fprintf(stderr, "Error: %s\n", *getResp.Msg.ErrorMessage)
					}
					if getResp.Msg.DurationSeconds != nil {
						fmt.Fprintf(stderr, "Duration: %.2f seconds\n", *getResp.Msg.DurationSeconds)
					}
					return fmt.Errorf("remote session exited with code %d", exitCode)
				} else {
					fmt.Fprintf(stdout, "\n✓ Remote session exited successfully (exit code 0)\n")
					fmt.Fprintf(stdout, "Session ID: %s\n", sessionID)
					if getResp.Msg.DurationSeconds != nil {
						fmt.Fprintf(stdout, "Duration: %.2f seconds\n", *getResp.Msg.DurationSeconds)
					}
					fmt.Fprintf(stdout, "View full session: https://depot.dev/orgs/%s/claude/sessions/%s\n", orgID, sessionID)
					return nil
				}

			}

			if !sessionRunning {
				req := &agentv1.DownloadClaudeSessionRequest{
					SessionId: sessionID,
				}
				if orgID != "" {
					req.OrganizationId = &orgID
				}

				_, err := client.DownloadClaudeSession(ctx, api.WithAuthentication(connect.NewRequest(req), token))
				if err == nil {
					sessionRunning = true
					fmt.Fprintf(stdout, "Remote session is running. Output will be saved and can be viewed at the URL above.\n")
					fmt.Fprintf(stdout, "You can resume this session later with: depot claude --resume %s\n", sessionID)
					fmt.Fprintf(stdout, "\nWaiting for session to complete...\n")
				}
			}
		}
	}
}

func checkRequiredClaudeSecrets(ctx context.Context, client agentv1connect.ClaudeServiceClient, token, orgID string, stderr io.Writer) error {
	req := &agentv1.ListSecretsRequest{}
	if orgID != "" {
		req.OrganizationId = &orgID
	}

	resp, err := client.ListSecrets(ctx, api.WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return nil
	}

	requiredSecrets := []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}
	hasRequiredSecret := false

	for _, secret := range resp.Msg.Secrets {
		for _, required := range requiredSecrets {
			if secret.Name == required {
				hasRequiredSecret = true
				break
			}
		}
		if hasRequiredSecret {
			break
		}
	}

	if !hasRequiredSecret {
		return fmt.Errorf(`Claude authentication secret required.

Please add one of the following secrets:
  - ANTHROPIC_API_KEY: Your Anthropic API key
  - ANTHROPIC_AUTH_TOKEN: Your Anthropic API key
  - CLAUDE_CODE_OAUTH_TOKEN: Your Claude Code OAuth token

To add a secret, run:
  depot claude secrets add <secret> <your-api-key>`)
	}

	return nil
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
