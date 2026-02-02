package sandbox

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/depot/cli/pkg/ssh"
	"github.com/spf13/cobra"
)

type listOptions struct {
	output string
	token  string
	orgID  string
	all    bool // Show all sandboxes, not just running (non-interactive)
	stdout io.Writer
	stderr io.Writer
}

// NewCmdList creates the sandbox list subcommand
func NewCmdList() *cobra.Command {
	opts := &listOptions{
		output: "",
		stdout: os.Stdout,
		stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sandboxes",
		Long: `List running sandboxes for your organization in interactive mode.

By default, shows an interactive table of running sandboxes where you can:
  - Use arrow keys to navigate
  - Press Enter to connect to a sandbox via SSH
  - Press k to kill a sandbox
  - Press a to toggle showing all sandboxes (including completed)
  - Press r to refresh the list
  - Press q or Esc to quit

Use --all to show all sandboxes (including completed) in non-interactive mode.
Use --output to specify a format (table, json, csv) for non-interactive output.`,
		Example: `  # Interactive mode (default) - select and manage running sandboxes
  depot sandbox list

  # List all sandboxes (including completed) as a table
  depot sandbox list --all

  # Output all sandboxes as JSON
  depot sandbox list --output json

  # Output all sandboxes as CSV
  depot sandbox list --output csv`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.output, "output", "", "Output format (table, json, csv) - disables interactive mode")
	cmd.Flags().StringVar(&opts.token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&opts.orgID, "org", "", "Organization ID")
	cmd.Flags().BoolVarP(&opts.all, "all", "a", false, "Show all sandboxes (including completed) in non-interactive table format")

	return cmd
}

type sandboxOutput struct {
	SandboxID   string `json:"sandbox_id"`
	SessionID   string `json:"session_id"`
	Status      string `json:"status"`
	SSHEnabled  bool   `json:"ssh_enabled"`
	TmateSSHURL string `json:"tmate_ssh_url,omitempty"`
	TmateWebURL string `json:"tmate_web_url,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func runList(ctx context.Context, opts *listOptions) error {
	token, err := helpers.ResolveOrgAuth(ctx, opts.token)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}

	// Check environment variable first, then config file
	if opts.orgID == "" {
		opts.orgID = os.Getenv("DEPOT_ORG_ID")
	}
	if opts.orgID == "" {
		opts.orgID = config.GetCurrentOrganization()
	}

	sandboxClient := api.NewSandboxClient()

	// Interactive mode is the default (unless --output or --all is specified)
	if opts.output == "" && !opts.all {
		return runInteractiveList(token, opts.orgID, sandboxClient)
	}

	req := &agentv1.ListSandboxsRequest{}

	res, err := sandboxClient.ListSandboxs(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.orgID))
	if err != nil {
		return fmt.Errorf("unable to list sandboxes: %w", err)
	}

	sandboxes := res.Msg.Sandboxes

	// Convert to output format
	outputs := make([]sandboxOutput, 0, len(sandboxes))
	for _, sb := range sandboxes {
		status := "running"
		if sb.CompletedAt != nil {
			status = "completed"
		} else if sb.StartedAt == nil {
			status = "pending"
		}

		out := sandboxOutput{
			SandboxID:  sb.SandboxId,
			SessionID:  sb.SessionId,
			Status:     status,
			SSHEnabled: (sb.SshEnabled != nil && *sb.SshEnabled) || sb.TmateSshUrl != nil,
			CreatedAt:  sb.CreatedAt.AsTime().Format("2006-01-02 15:04:05"),
		}
		if sb.TmateSshUrl != nil {
			out.TmateSSHURL = *sb.TmateSshUrl
		}
		if sb.TmateWebUrl != nil {
			out.TmateWebURL = *sb.TmateWebUrl
		}
		outputs = append(outputs, out)
	}

	// Default to table output for --all flag
	output := opts.output
	if output == "" {
		output = "table"
	}

	switch output {
	case "json":
		return outputJSON(outputs, opts.stdout)
	case "csv":
		return outputCSV(outputs, opts.stdout)
	default:
		return outputTable(outputs, opts.stdout)
	}
}

func outputJSON(sandboxes []sandboxOutput, w io.Writer) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(sandboxes)
}

func outputCSV(sandboxes []sandboxOutput, w io.Writer) error {
	writer := csv.NewWriter(w)
	defer writer.Flush()

	// Header
	if err := writer.Write([]string{"sandbox_id", "session_id", "status", "ssh_enabled", "tmate_ssh_url", "tmate_web_url", "created_at"}); err != nil {
		return err
	}

	for _, sb := range sandboxes {
		sshEnabled := "false"
		if sb.SSHEnabled {
			sshEnabled = "true"
		}
		if err := writer.Write([]string{
			sb.SandboxID,
			sb.SessionID,
			sb.Status,
			sshEnabled,
			sb.TmateSSHURL,
			sb.TmateWebURL,
			sb.CreatedAt,
		}); err != nil {
			return err
		}
	}

	return nil
}

func outputTable(sandboxes []sandboxOutput, w io.Writer) error {
	if len(sandboxes) == 0 {
		fmt.Fprintln(w, "No sandboxes found.")
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer tw.Flush()

	fmt.Fprintln(tw, "SANDBOX ID\tSESSION ID\tSTATUS\tSSH\tCREATED")
	for _, sb := range sandboxes {
		sshStatus := "no"
		if sb.SSHEnabled {
			sshStatus = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			sb.SandboxID,
			sb.SessionID,
			sb.Status,
			sshStatus,
			sb.CreatedAt,
		)
	}

	return nil
}

// Interactive mode implementation

var baseStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("240"))

var helpStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("241"))

type sandboxAction int

const (
	actionNone sandboxAction = iota
	actionConnect
	actionKill
)

type sandboxModel struct {
	table   table.Model
	columns []table.Column

	client agentv1connect.SandboxServiceClient
	token  string
	orgID  string

	// Map of sandbox ID to SSH URL for lookup after selection
	sshURLs map[string]string

	// Selected sandbox info for action
	selectedSandboxID string
	selectedSessionID string
	selectedSSHURL    string
	action            sandboxAction

	showAll bool // Toggle to show all sandboxes vs just running
	err     error
}

func newSandboxModel(token, orgID string, client agentv1connect.SandboxServiceClient) sandboxModel {
	columns := []table.Column{
		{Title: "Sandbox ID", Width: 20},
		{Title: "Session ID", Width: 36},
		{Title: "Status", Width: 10},
		{Title: "SSH", Width: 5},
		{Title: "Created", Width: 20},
	}

	styles := table.DefaultStyles()
	styles.Header = styles.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		BorderBottom(true).
		Bold(false)

	styles.Selected = styles.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(false)

	tbl := table.New(
		table.WithColumns(columns),
		table.WithFocused(true),
		table.WithStyles(styles),
	)

	return sandboxModel{
		table:   tbl,
		columns: columns,
		client:  client,
		token:   token,
		orgID:   orgID,
		sshURLs: make(map[string]string),
	}
}

func (m sandboxModel) Init() tea.Cmd {
	return m.loadSandboxes()
}

func (m sandboxModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		}

		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "r":
			return m, m.loadSandboxes()
		case "a":
			m.showAll = !m.showAll
			return m, m.loadSandboxes()
		case "enter":
			if len(m.table.Rows()) == 0 {
				return m, nil
			}
			row := m.table.SelectedRow()
			m.selectedSandboxID = row[0]
			m.selectedSessionID = row[1]
			m.selectedSSHURL = m.sshURLs[row[0]] // Look up SSH URL from map
			m.action = actionConnect
			return m, tea.Quit
		case "k":
			if len(m.table.Rows()) == 0 {
				return m, nil
			}
			row := m.table.SelectedRow()
			m.selectedSandboxID = row[0]
			m.selectedSessionID = row[1]
			m.action = actionKill
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		h, v := baseStyle.GetFrameSize()
		m.table.SetHeight(msg.Height - v - 5)
		m.table.SetWidth(msg.Width - h)

		// Adjust column widths to fit window
		colWidth := 0
		for _, col := range m.columns {
			colWidth += col.Width
		}
		remainingWidth := msg.Width - colWidth
		if remainingWidth > 0 {
			m.columns[1].Width += remainingWidth - h - 10
			m.table.SetColumns(m.columns)
		}

	case sandboxRowsMsg:
		m.err = nil
		m.sshURLs = msg.sshURLs

		var selectedRow table.Row
		if len(m.table.Rows()) > 0 {
			selectedRow = m.table.SelectedRow()
		}

		m.table.SetRows(msg.rows)

		// Preserve selection
		if len(selectedRow) > 0 {
			for i, row := range msg.rows {
				if row[0] == selectedRow[0] {
					m.table.SetCursor(i)
					break
				}
			}
		}

		return m, refreshTimer()

	case errMsg:
		m.err = msg.error
		return m, refreshTimer()

	case tickMsg:
		return m, m.loadSandboxes()
	}

	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m sandboxModel) View() string {
	// Show current filter mode
	filterText := "showing: running"
	if m.showAll {
		filterText = "showing: all"
	}
	header := helpStyle.Render(filterText) + "\n"

	s := header + baseStyle.Render(m.table.View()) + "\n"
	if m.err != nil {
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("Error: "+m.err.Error()) + "\n"
	}
	if len(m.table.Rows()) == 0 && m.err == nil {
		if m.showAll {
			s += helpStyle.Render("No sandboxes found. Press r to refresh or q to quit.") + "\n"
		} else {
			s += helpStyle.Render("No running sandboxes found. Press a to show all, r to refresh, or q to quit.") + "\n"
		}
	} else {
		s += helpStyle.Render("↑/↓: navigate • enter: connect • k: kill • a: toggle all • r: refresh • q: quit") + "\n"
	}
	return s
}

// Message types for BubbleTea
type sandboxRowsMsg struct {
	rows    []table.Row
	sshURLs map[string]string
}
type errMsg struct{ error }
type tickMsg struct{}

func (m sandboxModel) loadSandboxes() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		req := &agentv1.ListSandboxsRequest{}
		res, err := m.client.ListSandboxs(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), m.token, m.orgID))
		if err != nil {
			return errMsg{err}
		}

		rows := []table.Row{}
		sshURLs := make(map[string]string)

		for _, sb := range res.Msg.Sandboxes {
			// Filter based on showAll flag
			isRunning := sb.CompletedAt == nil && sb.StartedAt != nil
			if !m.showAll && !isRunning {
				continue
			}

			status := "running"
			if sb.CompletedAt != nil {
				status = "completed"
			} else if sb.StartedAt == nil {
				status = "pending"
			}

			sshStatus := "no"
			if (sb.SshEnabled != nil && *sb.SshEnabled) || sb.TmateSshUrl != nil {
				sshStatus = "yes"
				if sb.TmateSshUrl != nil {
					sshURLs[sb.SandboxId] = *sb.TmateSshUrl
				}
			}

			createdAt := sb.CreatedAt.AsTime().Format("2006-01-02 15:04:05")

			rows = append(rows, table.Row{
				sb.SandboxId,
				sb.SessionId,
				status,
				sshStatus,
				createdAt,
			})
		}

		return sandboxRowsMsg{rows: rows, sshURLs: sshURLs}
	}
}

func refreshTimer() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

func runInteractiveList(token, orgID string, client agentv1connect.SandboxServiceClient) error {
	m := newSandboxModel(token, orgID, client)

	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return fmt.Errorf("interactive mode error: %w", err)
	}

	model, ok := final.(sandboxModel)
	if !ok {
		return fmt.Errorf("unexpected model type: %T", final)
	}

	// Handle selected action
	switch model.action {
	case actionConnect:
		if model.selectedSSHURL == "" {
			return fmt.Errorf("sandbox %s does not have SSH enabled", model.selectedSandboxID)
		}
		ssh.PrintConnecting(model.selectedSSHURL, model.selectedSessionID, "depot sandbox", os.Stdout)
		return ssh.ExecSSH(model.selectedSSHURL)

	case actionKill:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		req := &agentv1.KillSandboxRequest{
			SandboxId: model.selectedSandboxID,
		}
		_, err := client.KillSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
		if err != nil {
			return fmt.Errorf("unable to kill sandbox: %w", err)
		}
		fmt.Printf("Sandbox %s has been terminated.\n", model.selectedSandboxID)
	}

	return nil
}
