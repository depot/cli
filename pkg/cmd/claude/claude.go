package claude

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/spf13/cobra"
)

// Runs Claude with the arguments given, along with some custom logic for saving and storing session data
// Unfortunately, we need to manually parse flags to allow passing argv to Claude
func NewCmdClaude() *cobra.Command {
	var (
		saveTag    string
		resumeTag  string
		orgID      string
		token      string
		retryCount = 3
		retryDelay = 2 * time.Second
	)

	cmd := &cobra.Command{
		Use:   "claude [flags] [claude args...]",
		Short: "Run claude with automatic session persistence",
		Long: `Run claude CLI with automatic session saving and resuming via Depot.
		
Sessions are stored by Depot and can be resumed by tag.
When using --save-tag, the session will be uploaded on exit.
When using --resume-tag, the previous session will be downloaded before starting.

Organization ID is required and can be specified via:
- --org flag
- DEPOT_ORG_ID environment variable

Authentication token can be specified via:
- --token flag
- DEPOT_TOKEN environment variable
- depot login command`,
		Example: `
  # Interactive usage - run claude and save session
  depot claude --save-tag feature-branch
  
  # Non-interactive usage - use -p flag for scripts
  depot claude --save-tag feature-branch -p "implement user authentication"
  
  # Resume a session and provide prompt
  depot claude --resume-tag feature-branch -p "add error handling"
  
  # Use in a script with piped input
  cat code.py | depot claude -p "review this code" --save-tag code-review
  
  # Set an organization with --org flag
  depot claude --org different-org-id --save-tag team-session -p "create API endpoint"
  
  # Use a specific token
  depot claude --token my-api-token --save-tag secure-session -p "analyze security logs"`,
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			claudeArgs := []string{}
			for i := 0; i < len(args); i++ {
				arg := args[i]
				switch arg {
				case "--save-tag":
					if i+1 < len(args) {
						saveTag = args[i+1]
						i++
					}
				case "--resume-tag":
					if i+1 < len(args) {
						resumeTag = args[i+1]
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
				default:
					if strings.HasPrefix(arg, "--save-tag=") {
						saveTag = strings.TrimPrefix(arg, "--save-tag=")
					} else if strings.HasPrefix(arg, "--resume-tag=") {
						resumeTag = strings.TrimPrefix(arg, "--resume-tag=")
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

			apiURL := os.Getenv("DEPOT_API_URL")
			if apiURL == "" {
				apiURL = "https://api.depot.dev"
			}

			if orgID == "" {
				orgID = os.Getenv("DEPOT_ORG_ID")
			}
			if orgID == "" {
				return fmt.Errorf("organization ID is required. Set DEPOT_ORG_ID environment variable or use --org flag")
			}

			client := agentv1connect.NewClaudeServiceClient(
				http.DefaultClient,
				apiURL,
			)

			claudePath, err := exec.LookPath("claude")
			if err != nil {
				return fmt.Errorf("claude CLI not found in PATH: %w", err)
			}

			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home directory: %w", err)
			}
			sessionDir := filepath.Join(homeDir, ".claude", "projects")

			var sessionID string
			if resumeTag != "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get working directory: %w", err)
				}

				sessionID, err = resumeSession(ctx, client, token, resumeTag, sessionDir, cwd, orgID, retryCount, retryDelay)
				if err != nil {
					return fmt.Errorf("failed to resume session: %w", err)
				}

				claudeArgs = append([]string{"--resume", sessionID}, claudeArgs...)
			}

			claudeCmd := exec.Command(claudePath, claudeArgs...)
			claudeCmd.Stdin = os.Stdin
			claudeCmd.Stdout = os.Stdout
			claudeCmd.Stderr = os.Stderr
			claudeCmd.Env = os.Environ()

			err = claudeCmd.Run()

			if saveTag != "" {
				cwd, cwdErr := os.Getwd()
				if cwdErr != nil {
					return fmt.Errorf("failed to get working directory: %w", cwdErr)
				}

				sessionFile, findErr := findLatestSessionFile(sessionDir, cwd)
				if findErr != nil {
					return fmt.Errorf("failed to find session file: %w", findErr)
				}

				if saveErr := saveSession(ctx, client, token, saveTag, sessionFile, retryCount, retryDelay, orgID); saveErr != nil {
					return fmt.Errorf("failed to save session: %w", saveErr)
				}

				fmt.Fprintf(os.Stderr, "\n✓ Session saved with tag: %s\n", saveTag)
			}

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
		req.Header().Set("Authorization", "Bearer "+token)

		resp, lastErr = client.DownloadClaudeSession(ctx, req)
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

	fmt.Fprintf(os.Stderr, "✓ Resumed session with tag: %s\n", identifier)
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
		req.Header().Set("Authorization", "Bearer "+token)

		resp, err := client.UploadClaudeSession(ctx, req)
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

func convertPathToProjectName(path string) string {
	// Convert absolute path to project name format used by claude
	// e.g., /Users/billy/Work -> -Users-billy-Work
	cleaned := filepath.Clean(path)
	// Replace all path separators with dashes
	return strings.ReplaceAll(cleaned, string(filepath.Separator), "-")
}
