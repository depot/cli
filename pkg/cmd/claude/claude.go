package claude

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
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
		retryCount      = 3
		retryDelay      = 2 * time.Second
		createdBranch   bool
	)

	cmd := &cobra.Command{
		Use:   "claude [flags] [claude args...]",
		Short: "Run claude with automatic session persistence",
		Long: `Run claude CLI with automatic session saving and resuming via Depot.

Sessions are stored by Depot and can be resumed by session ID.
The session is always uploaded on exit.

When starting a new session in a git repository, depot claude will:
- Automatically create a new git branch named after the session ID
- Commit any uncommitted changes before the session closes
- Push the branch to the remote repository (if configured)

When using --resume, Depot will first check for a local session file,
and if not found, will attempt to download it from Depot's servers.

All flags not recognized by depot are passed directly through to the claude CLI.
This includes claude flags like -p, --model, etc.`,
		Example: `
  # Interactive usage - run claude and save session
  depot claude --session-id feature-branch
  
  # Non-interactive usage - claude's -p flag is passed through
  depot claude --session-id feature-branch -p "implement user authentication"
  
  # Mix depot flags (--session-id) with claude flags (-p, --model)
  depot claude --session-id older-claude-pr-9953 --model claude-3-opus-20240229 -p "write tests"
  
  # Resume a session by ID
  depot claude --resume feature-branch -p "add error handling"
  depot claude --resume 09b15b34-2df4-48ae-9b9e-1de0aa09e43f -p "continue where we left off"
  depot claude --resume abc123def456
  
  # Use in a script with piped input (claude's -p flag)
  cat code.py | depot claude -p "review this code" --session-id code-review
  
  # The --org flag is only required if you're a member of multiple organizations
  depot claude --org different-org-id --session-id team-session -p "create API endpoint"`,
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
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

			token, err := helpers.ResolveToken(ctx, token)
			if err != nil {
				return err
			}
			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			if orgID == "" {
				orgID = os.Getenv("DEPOT_ORG_ID")
			}

			client := api.NewClaudeClient()

			// early auth check to prevent starting Claude if saving or resuming will fail
			if err := verifyAuthentication(ctx, client, token, orgID); err != nil {
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

			// Check if we're in a git repository
			isGitRepo := isGitRepository(ctx, cwd)
			if isGitRepo && resumeSessionID == "" {
				// Generate session ID if not provided
				if sessionID == "" {
					sessionID = generateSessionID()
				}
				
				// Create and checkout new branch using session ID as branch name
				if err := createAndCheckoutBranch(ctx, cwd, sessionID); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to create git branch: %v\n", err)
				} else {
					createdBranch = true
					fmt.Fprintf(os.Stderr, "Created and checked out git branch: %s\n", sessionID)
				}
			}

			sessionIDChan := make(chan sessionIDUpdate, 1)

			var claudeSessionID string
			if resumeSessionID != "" {
				if sessionID == "" {
					sessionID = resumeSessionID
				}

				resumeParams := ResumeSessionParams{
					Client:     client,
					Token:      token,
					SessionID:  resumeSessionID,
					SessionDir: sessionDir,
					Cwd:        cwd,
					OrgID:      orgID,
					RetryCount: retryCount,
					RetryDelay: retryDelay,
				}
				claudeSessionID, err = resumeSession(ctx, resumeParams)
				if err != nil {
					return fmt.Errorf("session '%s' not found remotely: %w", resumeSessionID, err)
				}
				// Send initial session ID to channel
				sessionIDChan <- sessionIDUpdate{id: claudeSessionID}

				switch output {
				case "json":
					fmt.Fprintf(os.Stdout, `{"action":"opened","session_id":"%s"}`+"\n", resumeSessionID)
				case "csv":
					fmt.Fprintf(os.Stdout, "action,session_id\nopened,%s\n", resumeSessionID)
				default:
					fmt.Fprintf(os.Stdout, "Opened Claude session from Depot with ID: %s\n", resumeSessionID)
				}
				claudeArgs = append(claudeArgs, "--resume", claudeSessionID)
			}

			claudeCtx, claudeCtxCancel := context.WithCancel(ctx)
			defer claudeCtxCancel()

			claudeCmd := exec.CommandContext(claudeCtx, claudePath, claudeArgs...)
			claudeCmd.Stdin = os.Stdin
			claudeCmd.Stdout = os.Stdout
			claudeCmd.Stderr = os.Stderr
			claudeCmd.Env = os.Environ()

			if err := claudeCmd.Start(); err != nil {
				return fmt.Errorf("failed to start claude: %w", err)
			}

			projectDir := filepath.Join(sessionDir, convertPathToProjectName(cwd))
			go func() {
				continuousParams := ContinuousSaveParams{
					ProjectDir:    projectDir,
					Client:        client,
					Token:         token,
					SessionID:     sessionID,
					SessionIDChan: sessionIDChan,
					OrgID:         orgID,
				}
				if err := continuouslySaveSessionFile(claudeCtx, continuousParams); err != nil {
					fmt.Fprintf(os.Stderr, "\nFailed to continuously save session file: %s", err)
				}
			}()

			claudeErr := claudeCmd.Wait()
			claudeCtxCancel()

			// Handle git cleanup before saving session
			if isGitRepo && createdBranch {
				if err := handleGitCleanup(ctx, cwd, sessionID); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to commit changes: %v\n", err)
				}
			}

			select {
			case update := <-sessionIDChan:
				claudeSessionID = update.id
			default:
			}

			var sessionFilePath string

			if claudeSessionID != "" {
				sessionFilePath = filepath.Join(projectDir, fmt.Sprintf("%s.jsonl", claudeSessionID))
			} else {
				sessionFilePath, err = findLatestSessionFile(sessionDir, cwd)
				if err != nil {
					return fmt.Errorf("failed to find session file: %w", err)
				}
				claudeSessionID = filepath.Base(strings.TrimSuffix(sessionFilePath, ".jsonl"))
			}

			if sessionID == "" {
				sessionID = filepath.Base(strings.TrimSuffix(sessionFilePath, ".jsonl"))
			}

			saveParams := SaveSessionParams{
				Client:          client,
				Token:           token,
				SessionID:       sessionID,
				ClaudeSessionID: claudeSessionID,
				SessionFilePath: sessionFilePath,
				RetryCount:      retryCount,
				RetryDelay:      retryDelay,
				OrgID:           orgID,
			}
			saveErr := saveSession(ctx, saveParams)
			if saveErr != nil {
				return fmt.Errorf("failed to save session: %w", saveErr)
			}

			switch output {
			case "json":
				fmt.Fprintf(os.Stdout, `{"action":"saved","session_id":"%s"}`+"\n", sessionID)
			case "csv":
				fmt.Fprintf(os.Stdout, "action,session_id\nsaved,%s\n", sessionID)
			default:
				fmt.Fprintf(os.Stdout, "Claude session saved to Depot with ID: %s\n", sessionID)
			}

			return claudeErr
		},
	}

	cmd.Flags().String("session-id", "", "Custom session ID for saving")
	cmd.Flags().String("resume", "", "Resume a session by ID")
	cmd.Flags().String("org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().String("token", "", "Depot API token")
	cmd.Flags().String("output", "", "Output format (json, csv)")

	return cmd
}

type ResumeSessionParams struct {
	Client     agentv1connect.ClaudeServiceClient
	Token      string
	SessionID  string
	SessionDir string
	Cwd        string
	OrgID      string
	RetryCount int
	RetryDelay time.Duration
}

func resumeSession(ctx context.Context, params ResumeSessionParams) (string, error) {
	var resp *connect.Response[agentv1.DownloadClaudeSessionResponse]
	var lastErr error

	for i := range params.RetryCount {
		if i > 0 {
			time.Sleep(params.RetryDelay)
		}

		req := &agentv1.DownloadClaudeSessionRequest{
			SessionId:      params.SessionID,
			OrganizationId: new(string),
		}
		if params.OrgID != "" {
			req.OrganizationId = &params.OrgID
		}

		resp, lastErr = params.Client.DownloadClaudeSession(ctx, api.WithAuthentication(connect.NewRequest(req), params.Token))
		if lastErr == nil {
			break
		}
	}

	if lastErr != nil {
		return "", fmt.Errorf("failed after %d retries: %w", params.RetryCount, lastErr)
	}

	projectDir := filepath.Join(params.SessionDir, convertPathToProjectName(params.Cwd))

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

type SaveSessionParams struct {
	Client          agentv1connect.ClaudeServiceClient
	Token           string
	SessionID       string
	ClaudeSessionID string
	SessionFilePath string
	RetryCount      int
	RetryDelay      time.Duration
	OrgID           string
}

func saveSession(ctx context.Context, params SaveSessionParams) error {
	data, err := os.ReadFile(params.SessionFilePath)
	if err != nil {
		return fmt.Errorf("failed to read session file: %w", err)
	}

	var lastErr error
	for i := range params.RetryCount {
		if i > 0 {
			time.Sleep(params.RetryDelay)
		}

		req := &agentv1.UploadClaudeSessionRequest{
			SessionData:    data,
			SessionId:      params.SessionID,
			OrganizationId: new(string),
			// TODO(billy)
			Summary:         new(string),
			ClaudeSessionId: params.ClaudeSessionID,
		}
		if params.OrgID != "" {
			req.OrganizationId = &params.OrgID
		}

		_, err := params.Client.UploadClaudeSession(ctx, api.WithAuthentication(connect.NewRequest(req), params.Token))
		if err != nil {
			lastErr = err
			continue
		}

		return nil
	}

	return fmt.Errorf("failed after %d retries: %w", params.RetryCount, lastErr)
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

type sessionIDUpdate struct {
	id string
}

type ContinuousSaveParams struct {
	ProjectDir    string
	Client        agentv1connect.ClaudeServiceClient
	Token         string
	SessionID     string
	SessionIDChan chan sessionIDUpdate
	OrgID         string
}

// continuouslySaveSessionFile monitors the project directory for new or changed session files and automatically saves them
func continuouslySaveSessionFile(ctx context.Context, params ContinuousSaveParams) error {
	if err := os.MkdirAll(params.ProjectDir, 0755); err != nil {
		return fmt.Errorf("failed to create project directory: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(params.ProjectDir); err != nil {
		return fmt.Errorf("failed to watch directory: %w", err)
	}

	var sessionFilePath string
	var claudeSessionID string

	select {
	case update := <-params.SessionIDChan:
		claudeSessionID = update.id
		sessionFilePath = filepath.Join(params.ProjectDir, fmt.Sprintf("%s.jsonl", claudeSessionID))
	default:
	}

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

			sessionID := params.SessionID
			if sessionID == "" {
				sessionID = filepath.Base(strings.TrimSuffix(sessionFilePath, ".jsonl"))
			}

			if claudeSessionID == "" {
				claudeSessionID = filepath.Base(strings.TrimSuffix(sessionFilePath, ".jsonl"))
				select {
				case params.SessionIDChan <- sessionIDUpdate{id: claudeSessionID}:
				default:
				}
			}

			saveParams := SaveSessionParams{
				Client:          params.Client,
				Token:           params.Token,
				SessionID:       sessionID,
				ClaudeSessionID: claudeSessionID,
				SessionFilePath: sessionFilePath,
				RetryCount:      3,
				RetryDelay:      2 * time.Second,
				OrgID:           params.OrgID,
			}
			// if the continuous save fails, it doesn't matter much. this is really only for the live view of the conversation
			_ = saveSession(ctx, saveParams)

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

// isGitRepository checks if the current directory is a git repository
func isGitRepository(ctx context.Context, dir string) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	err := cmd.Run()
	return err == nil
}

// createAndCheckoutBranch creates a new git branch and checks it out
func createAndCheckoutBranch(ctx context.Context, dir, branchName string) error {
	// Create branch
	cmd := exec.CommandContext(ctx, "git", "checkout", "-b", branchName)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create branch: %w", err)
	}
	return nil
}

// handleGitCleanup commits any uncommitted changes and pushes the branch
func handleGitCleanup(ctx context.Context, dir, sessionID string) error {
	// Check for uncommitted changes
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}

	if len(out) > 0 {
		// Add all changes
		cmd = exec.CommandContext(ctx, "git", "add", "-A")
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add changes: %w", err)
		}

		// Commit changes
		commitMsg := fmt.Sprintf("Claude session %s changes\n\nAutomatically committed by depot claude", sessionID)
		cmd = exec.CommandContext(ctx, "git", "commit", "-m", commitMsg)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to commit changes: %w", err)
		}

		fmt.Fprintf(os.Stderr, "Committed changes to git branch\n")
	}

	// Push branch to remote
	cmd = exec.CommandContext(ctx, "git", "push", "-u", "origin", sessionID)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Note: Branch not pushed to remote (no remote configured or push failed)\n")
	}

	return nil
}

// generateSessionID generates a random session ID
func generateSessionID() string {
	// Generate a more readable session ID with timestamp
	return fmt.Sprintf("claude-%s", time.Now().Format("20060102-150405"))
}
