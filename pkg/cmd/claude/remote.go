package claude

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
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
	res, err := client.StartRemoteSession(ctx, api.WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return fmt.Errorf("unable to start remote session: %w", err)
	}
	log.Printf("Session ID: %s", res.Msg.SessionId)
	return nil
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
