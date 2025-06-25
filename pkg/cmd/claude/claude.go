package claude

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/spf13/cobra"
)

// Runs Claude with the arguments given, along with some custom logic for saving and storing session data
// Unfortunately, we need to manually parse flags to allow passing argv to Claude
func NewCmdClaude() *cobra.Command {
	var (
		customSessionID string
		orgID           string
		token           string
		resumeSessionID string
		retryCount      = 3
		retryDelay      = 2 * time.Second
	)

	cmd := &cobra.Command{
		Use:   "claude [flags] [claude args...]",
		Short: "Run claude with automatic session persistence",
		Long: `Run claude CLI with automatic session saving and resuming via Depot.
		
Sessions are stored by Depot and can be resumed by session ID
The session is always uploaded on exit, though you can modify the name of the session with the --session-id flag
When using --resume <session-id>, Depot will first check for a local session file,
and if not found, will attempt to download it from Depot's servers.

Organization ID is required and can be specified via:
- --org flag
- DEPOT_ORG_ID environment variable

Authentication token can be specified via:
- --token flag
- DEPOT_TOKEN environment variable
- depot login command

All other flags are passed through to the claude CLI.`,
		Example: `
  # Interactive usage - run claude and save session
  depot claude --session-id feature-branch
  
  # Non-interactive usage - use -p flag for scripts
  depot claude --session-id feature-branch -p "implement user authentication"
  
  # Resume a session by ID
  depot claude --resume feature-branch -p "add error handling"
  depot claude --resume 09b15b34-2df4-48ae-9b9e-1de0aa09e43f -p "continue where we left off"
  depot claude --resume abc123def456
  
  # Use in a script with piped input
  cat code.py | depot claude -p "review this code" --session-id code-review
  
  # Set an organization with --org flag
  depot claude --org different-org-id --session-id team-session -p "create API endpoint"
  
  # Use a specific token
  depot claude --token my-api-token --session-id secure-session -p "analyze security logs"`,
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
						customSessionID = args[i+1]
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
				case "--resume":
					if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
						resumeSessionID = args[i+1]
						i++
					} else {
						claudeArgs = append(claudeArgs, arg)
					}
				default:
					if strings.HasPrefix(arg, "--session-id=") {
						customSessionID = strings.TrimPrefix(arg, "--session-id=")
					} else if strings.HasPrefix(arg, "--resume=") {
						resumeSessionID = strings.TrimPrefix(arg, "--resume=")
					} else if strings.HasPrefix(arg, "--org=") {
						orgID = strings.TrimPrefix(arg, "--org=")
					} else if strings.HasPrefix(arg, "--token=") {
						token = strings.TrimPrefix(arg, "--token=")
					} else {
						// Pass through any other flags to claude
						claudeArgs = append(claudeArgs, arg)
					}
				}
			}

			helpRequested := false
			for _, arg := range args {
				if arg == "--help" || arg == "-h" || arg == "help" {
					helpRequested = true
					break
				}
			}

			if helpRequested {
				return cmd.Help()
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
			if orgID == "" {
				return fmt.Errorf("organization ID is required. Set DEPOT_ORG_ID environment variable or use --org flag")
			}

			client := api.NewClaudeClient()

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

			if resumeSessionID != "" {
				projectDir := filepath.Join(sessionDir, convertPathToProjectName(cwd))
				localSessionFile := filepath.Join(projectDir, fmt.Sprintf("%s.jsonl", resumeSessionID))

				if _, err := os.Stat(localSessionFile); errors.Is(err, os.ErrNotExist) {
					sessionID, err := resumeSession(ctx, client, token, resumeSessionID, sessionDir, cwd, orgID, retryCount, retryDelay)
					if err != nil {
						return fmt.Errorf("session '%s' not found locally or remotely: %w", resumeSessionID, err)
					}
					resumeSessionID = sessionID
				}
				claudeArgs = append(claudeArgs, "--resume", resumeSessionID)
			}

			claudeCmd := exec.Command(claudePath, claudeArgs...)
			claudeCmd.Stdin = os.Stdin
			claudeCmd.Stdout = os.Stdout
			claudeCmd.Stderr = os.Stderr
			claudeCmd.Env = os.Environ()

			err = claudeCmd.Run()

			sessionFileName, findErr := findLatestSessionFile(sessionDir, cwd)
			if findErr != nil {
				return fmt.Errorf("failed to find session file: %w", findErr)
			}

			if customSessionID == "" {
				customSessionID = filepath.Base(strings.TrimSuffix(sessionFileName, ".jsonl"))
			}

			saveErr := saveSession(ctx, client, token, customSessionID, sessionFileName, retryCount, retryDelay, orgID)
			if saveErr != nil {
				return fmt.Errorf("failed to save session: %w", saveErr)
			}

			fmt.Fprintf(os.Stderr, "\n✓ Session saved with ID: %s\n", customSessionID)

			return err
		},
	}

	return cmd
}

func resumeSession(ctx context.Context, client agentv1connect.ClaudeServiceClient, token, identifier, sessionDir, cwd, orgID string, retryCount int, retryDelay time.Duration) (string, error) {
	var resp *connect.Response[agentv1.DownloadClaudeSessionResponse]
	var lastErr error

	for i := range retryCount {
		if i > 0 {
			time.Sleep(retryDelay)
		}

		req := connect.NewRequest(&agentv1.DownloadClaudeSessionRequest{
			Tag:            identifier,
			OrganizationId: orgID,
		})

		resp, lastErr = client.DownloadClaudeSession(ctx, api.WithAuthentication(req, token))
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

	sessionFile := filepath.Join(projectDir, fmt.Sprintf("%s.jsonl", resp.Msg.SessionId))
	out, err := os.Create(sessionFile)
	if err != nil {
		return "", fmt.Errorf("failed to create session file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, reader); err != nil {
		return "", fmt.Errorf("failed to write session file: %w", err)
	}

	fmt.Fprintf(os.Stderr, "✓ Resumed session with ID: %s\n", identifier)
	return resp.Msg.SessionId, nil
}

func saveSession(ctx context.Context, client agentv1connect.ClaudeServiceClient, token, tag, sessionFile string, retryCount int, retryDelay time.Duration, orgID string) error {
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return fmt.Errorf("failed to read session file: %w", err)
	}

	var lastErr error
	for i := range retryCount {
		if i > 0 {
			time.Sleep(retryDelay)
		}

		req := connect.NewRequest(&agentv1.UploadClaudeSessionRequest{
			Tag:            tag,
			SessionData:    data,
			OrganizationId: orgID,
		})

		resp, err := client.UploadClaudeSession(ctx, api.WithAuthentication(req, token))
		if err != nil {
			lastErr = err
			continue
		}

		if resp.Msg.Success {
			return nil
		}

		lastErr = fmt.Errorf("upload failed")
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
