package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
)

// Runs Claude with the arguments given, along with some custom logic for saving and storing session data
// Unfortunately, we need to manually parse flags to allow passing argv to Claude
func NewCmdClaude() *cobra.Command {
	var (
		sessionID       string
		orgID           string
		token           string
		resumeSessionID string
		output          string
		local           bool
		repository      string
		branch          string
		gitSecret       string
		wait            bool
	)

	cmd := &cobra.Command{
		Use:   "claude [flags] [claude args...]",
		Short: "Run claude with automatic session persistence",
		Long: `Run claude CLI with automatic session saving and resuming via Depot.

Sessions are stored by Depot and can be resumed by session ID.
The session is always uploaded on exit.

When using --resume, Depot will first check for a local session file,
and if not found, will attempt to download it from Depot's servers.

By default, Claude runs in a remote sandbox environment. Use --local to run locally instead.
When running remotely, you can specify a Git repository context with --repository to clone and work with remote code.

All flags not recognized by depot are passed directly through to the claude CLI.
This includes claude flags like -p, --model, etc.

Subcommands:
  list-sessions       List saved Claude sessions
  secrets            Manage secrets for Claude sandboxes (add, list, remove)`,
		Example: `
  # Save and resume sessions
  depot claude --session-id feature-branch
  depot claude --resume feature-branch

  # Run Claude in remote sandbox (default)
  depot claude --session-id sandbox-work

  # Run Claude locally instead of in sandbox
  depot claude --local --session-id local-work

  # Clone and work with a Git repository
  depot claude --repository https://github.com/user/repo.git --branch main

  # Use custom Git authentication secret
  depot claude --repository https://github.com/private/repo.git --git-secret MY_GIT_TOKEN

  # List saved sessions
  depot claude list-sessions


  # Manage secrets for Claude sandboxes
  depot claude secrets add GITHUB_TOKEN
  depot claude secrets list
  depot claude secrets remove GITHUB_TOKEN`,
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				switch args[0] {
				case "list-sessions":
					cmd.DisableFlagParsing = false
					subCmd := NewCmdClaudeListSessions()
					subCmd.SetArgs(args[1:])
					return subCmd.ExecuteContext(cmd.Context())
				case "secrets":
					cmd.DisableFlagParsing = false
					subCmd := NewCmdClaudeSecrets()
					subCmd.SetArgs(args[1:])
					return subCmd.ExecuteContext(cmd.Context())
				}
			}
			ctx := cmd.Context()

			claudeArgs := []string{}
			for i := 0; i < len(args); i++ {
				arg := args[i]
				switch arg {
				case "--session-id":
					if i+1 < len(args) {
						sessionID = args[i+1]
						i++
					}
				case "--org":
					if i+1 < len(args) {
						orgID = args[i+1]
						i++
					}
				case "--token":
					if i+1 < len(args) {
						token = args[i+1]
						i++
					}
				case "--output":
					if i+1 < len(args) {
						output = args[i+1]
						i++
					}
				case "--local":
					local = true
				case "--repository":
					if i+1 < len(args) {
						repository = args[i+1]
						i++
					}
				case "--branch":
					if i+1 < len(args) {
						branch = args[i+1]
						i++
					}
				case "--git-secret":
					if i+1 < len(args) {
						gitSecret = args[i+1]
						i++
					}
				case "--wait":
					wait = true
				case "--resume":
					if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
						resumeSessionID = args[i+1]
						i++
					} else {
						return fmt.Errorf("--resume flag requires a session ID")
					}
				default:
					if strings.HasPrefix(arg, "--session-id=") {
						sessionID = strings.TrimPrefix(arg, "--session-id=")
					} else if strings.HasPrefix(arg, "--resume=") {
						resumeSessionID = strings.TrimPrefix(arg, "--resume=")
					} else if strings.HasPrefix(arg, "--org=") {
						orgID = strings.TrimPrefix(arg, "--org=")
					} else if strings.HasPrefix(arg, "--token=") {
						token = strings.TrimPrefix(arg, "--token=")
					} else if strings.HasPrefix(arg, "--output=") {
						output = strings.TrimPrefix(arg, "--output=")
					} else if strings.HasPrefix(arg, "--repository=") {
						repository = strings.TrimPrefix(arg, "--repository=")
					} else if strings.HasPrefix(arg, "--branch=") {
						branch = strings.TrimPrefix(arg, "--branch=")
					} else if strings.HasPrefix(arg, "--git-secret=") {
						gitSecret = strings.TrimPrefix(arg, "--git-secret=")
					} else {
						// Pass through any other flags to claude
						claudeArgs = append(claudeArgs, arg)
					}
				}
			}

			helpRequested := false
			for _, arg := range args {
				if arg == "--help" || arg == "-h" {
					helpRequested = true
					break
				}
			}

			// show Depot help, then Claude's
			if helpRequested {
				if err := cmd.Help(); err != nil {
					return err
				}

				fmt.Fprintln(os.Stderr, "\n--- Claude CLI Help ---")
				claudeCmd := exec.CommandContext(ctx, "claude", "--help")
				claudeCmd.Stdout = os.Stderr
				claudeCmd.Stderr = os.Stderr
				return claudeCmd.Run()
			}

			// If org ID is not set, use the current organization from config
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			opts := &ClaudeSessionOptions{
				SessionID:       sessionID,
				OrgID:           orgID,
				Token:           token,
				ResumeSessionID: resumeSessionID,
				Output:          output,
				ClaudeArgs:      claudeArgs,
				Stdin:           os.Stdin,
				Stdout:          os.Stdout,
				Stderr:          os.Stderr,
			}

			if resumeSessionID != "" && sessionID == "" {
				sessionID = resumeSessionID
			}

			if !local {
				// Default repository to current git remote if not specified
				if repository == "" {
					gitRemoteCmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
					if output, err := gitRemoteCmd.Output(); err == nil {
						repository = strings.TrimSpace(string(output))
						fmt.Fprintf(os.Stderr, "Using current git remote repository: %s\n", repository)
					}
				}

				agentOpts := &AgentRemoteOptions{
					SessionID:       sessionID,
					OrgID:           orgID,
					Token:           token,
					ClaudeArgs:      claudeArgs,
					Repository:      repository,
					Branch:          branch,
					GitSecret:       gitSecret,
					ResumeSessionID: resumeSessionID,
					Wait:            wait,
					Stdin:           os.Stdin,
					Stdout:          os.Stdout,
					Stderr:          os.Stderr,
					AgentType:       "claude",
				}
				return RunAgentRemote(ctx, agentOpts)
			} else {
				return RunClaudeSession(ctx, opts)
			}
		},
	}

	cmd.Flags().String("session-id", "", "Custom session ID for saving")
	cmd.Flags().String("resume", "", "Resume a session by ID")
	cmd.Flags().String("org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().String("token", "", "Depot API token")
	cmd.Flags().String("output", "", "Output format (json, csv)")
	cmd.Flags().Bool("local", false, "Run Claude locally instead of in a remote sandbox")
	cmd.Flags().String("repository", "", "Git repository URL for remote context (format: https://github.com/user/repo.git)")
	cmd.Flags().String("branch", "", "Git branch to use (defaults to main)")
	cmd.Flags().String("git-secret", "", "Secret name containing Git credentials for private repositories (optional)")
	cmd.Flags().Bool("wait", false, "Wait for the remote Claude session to complete (by default exits after starting)")

	return cmd
}

// returns the session file UUID that claude should resume from
func resumeSession(ctx context.Context, client agentv1connect.ClaudeServiceClient, token, sessionID, sessionDir, cwd, orgID string, retryCount int, retryDelay time.Duration) (string, error) {
	var resp *connect.Response[agentv1.DownloadClaudeSessionResponse]
	var lastErr error

	for i := range retryCount {
		if i > 0 {
			time.Sleep(retryDelay)
		}

		req := &agentv1.DownloadClaudeSessionRequest{
			SessionId:      sessionID,
			OrganizationId: new(string),
		}
		if orgID != "" {
			req.OrganizationId = &orgID
		}

		resp, lastErr = client.DownloadClaudeSession(ctx, api.WithAuthentication(connect.NewRequest(req), token))
		if lastErr == nil {
			break
		}
	}

	if lastErr != nil {
		return "", fmt.Errorf("failed after %d retries: %w", retryCount, lastErr)
	}

	projectDir := filepath.Join(sessionDir, convertPathToProjectName(cwd))

	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create project directory: %w", err)
	}

	reader := bytes.NewReader(resp.Msg.SessionData)

	sessionFilePath := filepath.Join(projectDir, fmt.Sprintf("%s.jsonl", resp.Msg.ClaudeSessionId))
	out, err := os.Create(sessionFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to create session file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, reader); err != nil {
		return "", fmt.Errorf("failed to write session file: %w", err)
	}

	return resp.Msg.ClaudeSessionId, nil
}

func saveSession(ctx context.Context, client agentv1connect.ClaudeServiceClient, token, sessionID, sessionFilePath string, retryCount int, retryDelay time.Duration, orgID string) error {
	data, err := os.ReadFile(sessionFilePath)
	if err != nil {
		return fmt.Errorf("failed to read session file: %w", err)
	}

	summary := extractSummaryFromSession(data)

	var lastErr error
	for i := range retryCount {
		if i > 0 {
			time.Sleep(retryDelay)
		}

		claudeSessionID := filepath.Base(strings.TrimSuffix(sessionFilePath, ".jsonl"))

		req := &agentv1.UploadClaudeSessionRequest{
			SessionData:    data,
			SessionId:      sessionID,
			OrganizationId: new(string),
			Summary:        new(string),
			ClaudeSessionId: claudeSessionID,
		}
		if summary != "" {
			req.Summary = &summary
		}
		if orgID != "" {
			req.OrganizationId = &orgID
		}

		_, err := client.UploadClaudeSession(ctx, api.WithAuthentication(connect.NewRequest(req), token))
		if err != nil {
			lastErr = err
			continue
		}

		return nil
	}

	return fmt.Errorf("failed after %d retries: %w", retryCount, lastErr)
}

func findLatestSessionFile(sessionDir, cwd string) (string, error) {
	projectDir := filepath.Join(sessionDir, convertPathToProjectName(cwd))

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", fmt.Errorf("project directory %s not found: %w", projectDir, err)
	}

	var latestFile string
	var latestTime time.Time

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latestFile = filepath.Join(projectDir, entry.Name())
		}
	}

	if latestFile == "" {
		return "", fmt.Errorf("no session files found in %s", projectDir)
	}

	return latestFile, nil
}

var nonAlphanumericRegex = regexp.MustCompile(`[^a-zA-Z0-9]`)

func convertPathToProjectName(path string) string {
	// Convert absolute path to project name format used by claude
	// e.g., /Users/billy/Work -> -Users-billy-Work
	// e.g., /Users/jacobwgillespie/.dotfiles -> -Users-jacobwgillespie--dotfiles
	cleaned := filepath.Clean(path)

	// this matches Claude's implementation: B.replace(/[^a-zA-Z0-9]/g, "-"))
	return nonAlphanumericRegex.ReplaceAllString(cleaned, "-")
}

// continuouslySaveSessionFile monitors the project directory for new or changed session files and automatically saves them
func continuouslySaveSessionFile(ctx context.Context, projectDir string, client agentv1connect.ClaudeServiceClient, token, sessionID, orgID string) error {
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return fmt.Errorf("failed to create project directory: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(projectDir); err != nil {
		return fmt.Errorf("failed to watch directory: %w", err)
	}

	var sessionFilePath string
	var claudeSessionID string

	for {
		select {
		case <-ctx.Done():
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			changedFileAbsPath, err := filepath.Abs(event.Name)
			if err != nil {
				return fmt.Errorf("failed to create absolute path for file %s", event.Name)
			}

			if sessionFilePath == "" && filepath.Ext(changedFileAbsPath) == ".jsonl" {
				sessionFilePath = changedFileAbsPath
			} else if changedFileAbsPath != sessionFilePath {
				continue
			}

			if sessionID == "" {
				sessionID = filepath.Base(strings.TrimSuffix(sessionFilePath, ".jsonl"))
			}

			if claudeSessionID == "" {
				claudeSessionID = filepath.Base(strings.TrimSuffix(sessionFilePath, ".jsonl"))
			}

			// if the continuous save fails, it doesn't matter much. this is really only for the live view of the conversation
			_ = saveSession(ctx, client, token, sessionID, sessionFilePath, 3, 2*time.Second, orgID)

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nWarning: file watcher error: %v\n", err)
			}

		}
	}
}

type ClaudeSessionOptions struct {
	SessionID       string
	OrgID           string
	Token           string
	ResumeSessionID string
	Output          string
	ClaudeArgs      []string
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
}

func RunClaudeSession(ctx context.Context, opts *ClaudeSessionOptions) error {
	retryCount := 3
	retryDelay := 2 * time.Second

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

	// early auth check to prevent starting Claude if saving or resuming will fail
	if err := verifyAuthentication(ctx, client, token, opts.OrgID); err != nil {
		return err
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude CLI not found in PATH: %w", err)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	sessionDir := filepath.Join(homeDir, ".claude", "projects")

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	claudeArgs := slices.Clone(opts.ClaudeArgs)
	sessionID := opts.SessionID
	resumeSessionID := opts.ResumeSessionID

	if resumeSessionID != "" {
		claudeSessionID, err := resumeSession(ctx, client, token, resumeSessionID, sessionDir, cwd, opts.OrgID, retryCount, retryDelay)
		if err != nil {
			return fmt.Errorf("session '%s' not found remotely: %w", resumeSessionID, err)
		}

		switch opts.Output {
		case "json":
			fmt.Fprintf(opts.Stdout, `{"action":"opened","session_id":"%s"}`+"\n", resumeSessionID)
		case "csv":
			fmt.Fprintf(opts.Stdout, "action,session_id\nopened,%s\n", resumeSessionID)
		default:
			fmt.Fprintf(opts.Stdout, "Opened Claude session from Depot with ID: %s\n", resumeSessionID)
		}
		claudeArgs = append(claudeArgs, "--resume", claudeSessionID)
	}

	claudeCtx, claudeCtxCancel := context.WithCancel(ctx)
	defer claudeCtxCancel()

	claudeCmd := exec.CommandContext(claudeCtx, claudePath, claudeArgs...)
	claudeCmd.Stdin = opts.Stdin
	claudeCmd.Stdout = opts.Stdout
	claudeCmd.Stderr = opts.Stderr
	claudeCmd.Env = os.Environ()

	if err := claudeCmd.Start(); err != nil {
		return fmt.Errorf("failed to start claude: %w", err)
	}

	projectDir := filepath.Join(sessionDir, convertPathToProjectName(cwd))
	go func() {
		if err := continuouslySaveSessionFile(claudeCtx, projectDir, client, token, sessionID, opts.OrgID); err != nil {
			fmt.Fprintf(opts.Stdout, "\nFailed to continuously save session file: %s", err)
		}
	}()

	claudeErr := claudeCmd.Wait()
	claudeCtxCancel()

	sessionFileName, findErr := findLatestSessionFile(sessionDir, cwd)
	if findErr != nil {
		return fmt.Errorf("failed to find session file: %w", findErr)
	}

	if sessionID == "" {
		sessionID = filepath.Base(strings.TrimSuffix(sessionFileName, ".jsonl"))
	}

	saveErr := saveSession(ctx, client, token, sessionID, sessionFileName, retryCount, retryDelay, opts.OrgID)
	if saveErr != nil {
		return fmt.Errorf("failed to save session: %w", saveErr)
	}

	switch opts.Output {
	case "json":
		fmt.Fprintf(opts.Stdout, `{"action":"saved","session_id":"%s"}`+"\n", sessionID)
	case "csv":
		fmt.Fprintf(opts.Stdout, "action,session_id\nsaved,%s\n", sessionID)
	default:
		fmt.Fprintf(opts.Stdout, "Claude session saved to Depot with ID: %s\n", sessionID)
	}

	return claudeErr
}

// extractSummaryFromSession extracts the most recent summary from session data
func extractSummaryFromSession(data []byte) string {
	lines := bytes.Split(data, []byte{'\n'})

	// Reverse iterate to find the most recent summary
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}

		var parsed map[string]any
		if err := json.Unmarshal(line, &parsed); err != nil {
			continue
		}

		if parsed["type"] == "summary" {
			if summary, ok := parsed["summary"].(string); ok {
				return summary
			}
		}
	}

	return ""
}

// verifyAuthentication performs an early auth check by calling the list-sessions API
// this prevents starting Claude if authentication or organization access will fail
func verifyAuthentication(ctx context.Context, client agentv1connect.ClaudeServiceClient, token, orgID string) error {
	req := &agentv1.ListClaudeSessionsRequest{}
	if orgID != "" {
		req.OrganizationId = &orgID
	}

	_, err := client.ListClaudeSessions(ctx, api.WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	return nil
}
