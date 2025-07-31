package claude

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
)

var docStyle = lipgloss.NewStyle().Margin(1, 2)

func NewCmdClaudeListSessions() *cobra.Command {
	var (
		token     string
		orgID     string
		output    string
		pageToken string
	)

	cmd := &cobra.Command{
		Use:   "list-sessions",
		Short: "List saved Claude sessions",
		Long: `List all saved Claude sessions for the organization.

In interactive mode, pressing Enter on a session will start Claude with that session.`,
		Example: `
  # List sessions interactively
  depot claude list-sessions

  # List sessions in JSON format
  depot claude list-sessions --output json

  # List sessions in CSV format
  depot claude list-sessions --output csv`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token, err := helpers.ResolveProjectAuth(ctx, token)
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

			isInteractive := output == "" && helpers.IsTerminal()

			var allSessions []*agentv1.ClaudeSession
			currentPageToken := pageToken
			maxPages := 5
			pagesLoaded := 0

			for {
				reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()

				req := &agentv1.ListClaudeSessionsRequest{}
				if orgID != "" {
					req.OrganizationId = &orgID
				}
				if currentPageToken != "" {
					req.PageToken = &currentPageToken
				}

				resp, err := client.ListClaudeSessions(reqCtx, api.WithAuthentication(connect.NewRequest(req), token))
				if err != nil {
					return fmt.Errorf("failed to list sessions: %w", err)
				}

				allSessions = append(allSessions, resp.Msg.Sessions...)
				pagesLoaded++

				// In interactive mode, automatically fetch all pages up to limit
				if isInteractive && resp.Msg.NextPageToken != nil && *resp.Msg.NextPageToken != "" && pagesLoaded < maxPages {
					currentPageToken = *resp.Msg.NextPageToken
					continue
				}

				// For non-interactive mode, print next page token if present
				if !isInteractive && resp.Msg.NextPageToken != nil && *resp.Msg.NextPageToken != "" {
					fmt.Fprintf(os.Stderr, "Next page token: %s\n", *resp.Msg.NextPageToken)
				}

				// In interactive mode, warn if we hit the page limit
				if isInteractive && pagesLoaded >= maxPages && resp.Msg.NextPageToken != nil && *resp.Msg.NextPageToken != "" {
					fmt.Fprintf(os.Stderr, "Showing first %d pages of results. To see more, run:\n", maxPages)
					fmt.Fprintf(os.Stderr, "  depot claude list-sessions --page-token %s\n", *resp.Msg.NextPageToken)
				}

				break
			}

			sessions := allSessions

			sort.Slice(sessions, func(i, j int) bool {
				if sessions[i].UpdatedAt == nil {
					return false
				}
				if sessions[j].UpdatedAt == nil {
					return true
				}
				return sessions[i].UpdatedAt.AsTime().After(sessions[j].UpdatedAt.AsTime())
			})

			switch output {
			case "json":
				return sessionWriteJSON(sessions)
			case "csv":
				return sessionWriteCSV(sessions)
			}

			if !helpers.IsTerminal() {
				return sessionWriteCSV(sessions)
			}

			selectedSession, err := chooseSession(sessions)
			if err != nil {
				return err
			}

			if selectedSession == nil {
				return nil // User cancelled
			}

			resumeID := selectedSession.SessionId

			fmt.Print("\033[H\033[2J") // Clear screen

			opts := &ClaudeSessionOptions{
				ResumeSessionID: resumeID,
				OrgID:           orgID,
				Token:           token,
				Stdin:           os.Stdin,
				Stdout:          os.Stdout,
				Stderr:          os.Stderr,
			}

			return RunClaudeSession(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&orgID, "org", config.GetCurrentOrganization(), "Organization ID")
	cmd.Flags().StringVar(&output, "output", "", "Output format (json, csv)")

	return cmd
}

func chooseSession(sessions []*agentv1.ClaudeSession) (*agentv1.ClaudeSession, error) {
	if len(sessions) == 0 {
		fmt.Println("No Claude sessions found")
		return nil, nil
	}

	items := []list.Item{}
	for _, s := range sessions {
		summary := ""
		if s.Summary != nil {
			summary = *s.Summary
		}
		items = append(items, sessionItem{
			session: s,
			summary: summary,
		})
	}

	m := sessionListModel{
		list:     list.New(items, list.NewDefaultDelegate(), 0, 0),
		sessions: sessions,
		ctrlC:    false,
	}
	m.list.Title = "Choose a Claude session"

	p := tea.NewProgram(m, tea.WithAltScreen())

	final, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("error running program: %w", err)
	}

	finalModel, ok := final.(sessionListModel)
	if !ok {
		return nil, fmt.Errorf("final model is not the expected type")
	}

	if finalModel.ctrlC {
		os.Exit(1)
	}

	return finalModel.choice, nil
}

type sessionItem struct {
	session *agentv1.ClaudeSession
	summary string
}

func (i sessionItem) Title() string {
	return i.session.SessionId
}

func (i sessionItem) Description() string {
	var parts []string

	if i.summary != "" {
		parts = append(parts, i.summary)
	}

	if i.session.UpdatedAt != nil {
		parts = append(parts, fmt.Sprintf("Updated: %s", i.session.UpdatedAt.AsTime().Format("Jan 2, 2006 3:04 PM")))
	}

	if i.session.CreatedAt != nil {
		parts = append(parts, fmt.Sprintf("Created: %s", i.session.CreatedAt.AsTime().Format("Jan 2, 2006 3:04 PM")))
	}

	if len(parts) > 0 {
		return strings.Join(parts, " • ")
	}
	return ""
}

func (i sessionItem) FilterValue() string {
	return i.session.SessionId
}

type sessionListModel struct {
	list     list.Model
	sessions []*agentv1.ClaudeSession
	choice   *agentv1.ClaudeSession
	ctrlC    bool
}

func (m sessionListModel) Init() tea.Cmd {
	return nil
}

func (m sessionListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.ctrlC = true
			return m, tea.Quit
		}

		if msg.String() == "enter" {
			if i, ok := m.list.SelectedItem().(sessionItem); ok {
				m.choice = i.session
			}
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		h, v := docStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m sessionListModel) View() string {
	return docStyle.Render(m.list.View())
}

func sessionWriteJSON(sessions []*agentv1.ClaudeSession) error {
	type sessionJSON struct {
		SessionID string    `json:"session_id"`
		Summary   string    `json:"summary,omitempty"`
		UpdatedAt time.Time `json:"updated_at,omitempty"`
		CreatedAt time.Time `json:"created_at,omitempty"`
	}

	var jsonSessions []sessionJSON
	for _, s := range sessions {
		session := sessionJSON{
			SessionID: s.SessionId,
		}
		if s.Summary != nil {
			session.Summary = *s.Summary
		}
		if s.UpdatedAt != nil {
			session.UpdatedAt = s.UpdatedAt.AsTime()
		}
		if s.CreatedAt != nil {
			session.CreatedAt = s.CreatedAt.AsTime()
		}
		jsonSessions = append(jsonSessions, session)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(jsonSessions)
}

func sessionWriteCSV(sessions []*agentv1.ClaudeSession) error {
	w := csv.NewWriter(os.Stdout)
	if err := w.Write([]string{"SESSION_ID", "SUMMARY", "UPDATED_AT", "CREATED_AT"}); err != nil {
		return err
	}

	for _, s := range sessions {
		summary := ""
		if s.Summary != nil {
			summary = *s.Summary
		}
		updatedAt := ""
		if s.UpdatedAt != nil {
			updatedAt = s.UpdatedAt.AsTime().Format(time.RFC3339)
		}
		createdAt := ""
		if s.CreatedAt != nil {
			createdAt = s.CreatedAt.AsTime().Format(time.RFC3339)
		}
		record := []string{
			s.SessionId,
			summary,
			updatedAt,
			createdAt,
		}
		if err := w.Write(record); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}
