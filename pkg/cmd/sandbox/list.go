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
  - Press Enter to connect to a running sandbox via SSH
  - Press r to resume a completed sandbox
  - Press k to kill a sandbox
  - Press a to toggle showing all sandboxes (including completed)
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
	SandboxID    string `json:"sandbox_id"`
	SessionID    string `json:"session_id"`
	Status       string `json:"status"`
	SSHEnabled   bool   `json:"ssh_enabled"`
	SSHHost      string `json:"ssh_host,omitempty"`
	SSHPort      int32  `json:"ssh_port,omitempty"`
	TemplateName string `json:"template_name,omitempty"`
	Repository   string `json:"repository,omitempty"`
	CreatedAt    string `json:"created_at"`
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
			SSHEnabled: (sb.SshEnabled != nil && *sb.SshEnabled) || sb.SshHost != nil,
			CreatedAt:  sb.CreatedAt.AsTime().Format("2006-01-02 15:04:05"),
		}
		if sb.SshHost != nil {
			out.SSHHost = *sb.SshHost
		}
		if sb.SshPort != nil {
			out.SSHPort = *sb.SshPort
		}
		if sb.TemplateName != nil {
			out.TemplateName = *sb.TemplateName
		}
		if sb.Repository != nil {
			out.Repository = *sb.Repository
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
	if err := writer.Write([]string{"sandbox_id", "session_id", "status", "ssh_enabled", "ssh_host", "ssh_port", "template", "repository", "created_at"}); err != nil {
		return err
	}

	for _, sb := range sandboxes {
		sshEnabled := "false"
		if sb.SSHEnabled {
			sshEnabled = "true"
		}
		sshPort := ""
		if sb.SSHPort > 0 {
			sshPort = fmt.Sprintf("%d", sb.SSHPort)
		}
		if err := writer.Write([]string{
			sb.SandboxID,
			sb.SessionID,
			sb.Status,
			sshEnabled,
			sb.SSHHost,
			sshPort,
			sb.TemplateName,
			sb.Repository,
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

	fmt.Fprintln(tw, "SANDBOX ID\tSESSION ID\tSTATUS\tSSH\tTEMPLATE\tREPOSITORY\tCREATED")
	for _, sb := range sandboxes {
		sshStatus := "no"
		if sb.SSHEnabled {
			sshStatus = "yes"
		}
		templateName := "-"
		if sb.TemplateName != "" {
			templateName = sb.TemplateName
		}
		repository := "-"
		if sb.Repository != "" {
			repository = sb.Repository
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			sb.SandboxID,
			sb.SessionID,
			sb.Status,
			sshStatus,
			templateName,
			repository,
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
	Foreground(lipgloss.Color("248"))

type sandboxAction int

const (
	actionNone sandboxAction = iota
	actionConnect
	actionKill
	actionResume
)

type sandboxModel struct {
	table   table.Model
	columns []table.Column

	client agentv1connect.SandboxServiceClient
	token  string
	orgID  string

	// Map of session ID to sandbox ID for lookup after selection
	sandboxIDs map[string]string

	// Selected sandbox info for action
	selectedSandboxID string
	selectedSessionID string
	action            sandboxAction

	// Whether the selected sandbox has SSH available
	selectedHasSSH bool
	// Status of the selected sandbox (running, completed, pending)
	selectedStatus string

	showAll    bool // Toggle to show all sandboxes vs just running
	statusMsg  string
	killingSID string // sandbox ID currently being killed
	err        error
}

func newSandboxModel(token, orgID string, client agentv1connect.SandboxServiceClient) sandboxModel {
	columns := []table.Column{
		{Title: "Session ID", Width: 16},
		{Title: "Status", Width: 10},
		{Title: "SSH", Width: 5},
		{Title: "Template", Width: 16},
		{Title: "Repository", Width: 30},
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
		table:      tbl,
		columns:    columns,
		client:     client,
		token:      token,
		orgID:      orgID,
		sandboxIDs: make(map[string]string),
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
			if len(m.table.Rows()) == 0 {
				return m, nil
			}
			row := m.table.SelectedRow()
			if row[1] != "completed" {
				return m, nil
			}
			sessionID := row[0]
			m.selectedSessionID = sessionID
			m.selectedSandboxID = m.sandboxIDs[sessionID]
			m.selectedHasSSH = row[2] == "yes"
			m.selectedStatus = row[1]
			m.action = actionResume
			return m, tea.Quit
		case "a":
			m.showAll = !m.showAll
			return m, m.loadSandboxes()
		case "enter":
			if len(m.table.Rows()) == 0 {
				return m, nil
			}
			row := m.table.SelectedRow()
			sessionID := row[0]
			status := row[1]
			hasSSH := row[2] == "yes"
			m.selectedSessionID = sessionID
			m.selectedSandboxID = m.sandboxIDs[sessionID]
			m.selectedHasSSH = hasSSH
			m.selectedStatus = status

			switch status {
			case "running":
				if hasSSH {
					m.action = actionConnect
					return m, tea.Quit
				}
				m.statusMsg = fmt.Sprintf("Sandbox %s has no SSH. Use: depot sandbox exec %s <command>", sessionID, m.sandboxIDs[sessionID])
				return m, nil
			default:
				// completed or pending — no action on enter
				return m, nil
			}
		case "k":
			if len(m.table.Rows()) == 0 || m.killingSID != "" {
				return m, nil
			}
			row := m.table.SelectedRow()
			sessionID := row[0]
			sandboxID := m.sandboxIDs[sessionID]
			m.killingSID = sandboxID
			m.statusMsg = fmt.Sprintf("Killing sandbox %s...", sessionID)
			return m, m.killSandbox(sandboxID)
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
		m.sandboxIDs = msg.sandboxIDs

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

	case killedMsg:
		m.err = nil
		m.killingSID = ""
		m.statusMsg = fmt.Sprintf("Sandbox %s terminated.", msg.sandboxID)
		return m, m.loadSandboxes()

	case errMsg:
		m.err = msg.error
		m.killingSID = ""
		m.statusMsg = ""
		return m, refreshTimer()

	case tickMsg:
		m.statusMsg = ""
		return m, m.loadSandboxes()
	}

	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m sandboxModel) View() string {
	header := ""
	if m.statusMsg != "" {
		color := "229" // yellow for in-progress
		if m.killingSID == "" {
			color = "78" // green for completed
		}
		header = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(m.statusMsg) + "\n"
	}
	s := header + baseStyle.Render(m.table.View()) + "\n"
	if m.err != nil {
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("Error: "+m.err.Error()) + "\n"
	}
	if len(m.table.Rows()) == 0 && m.err == nil {
		if m.showAll {
			s += helpStyle.Render("No sandboxes found. Press q to quit.") + "\n"
		} else {
			s += helpStyle.Render("No running sandboxes found. Press a to show all or q to quit.") + "\n"
		}
	} else {
		s += helpStyle.Render("↑/↓: navigate • enter: connect • r: resume • k: kill • a: toggle all • q: quit") + "\n"
	}
	return s
}

// Message types for BubbleTea
type sandboxRowsMsg struct {
	rows       []table.Row
	sandboxIDs map[string]string
}
type errMsg struct{ error }
type tickMsg struct{}
type killedMsg struct{ sandboxID string }

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
		sandboxIDs := make(map[string]string)

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
			if (sb.SshEnabled != nil && *sb.SshEnabled) || sb.SshHost != nil {
				sshStatus = "yes"
			}

			sandboxIDs[sb.SessionId] = sb.SandboxId

			createdAt := sb.CreatedAt.AsTime().Format("2006-01-02 15:04:05")

			templateName := ""
			if sb.TemplateName != nil {
				templateName = *sb.TemplateName
			}
			repository := ""
			if sb.Repository != nil {
				repository = *sb.Repository
			}

			rows = append(rows, table.Row{
				sb.SessionId,
				status,
				sshStatus,
				templateName,
				repository,
				createdAt,
			})
		}

		return sandboxRowsMsg{rows: rows, sandboxIDs: sandboxIDs}
	}
}

func (m sandboxModel) killSandbox(sandboxID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		req := &agentv1.KillSandboxRequest{
			SandboxId: sandboxID,
		}
		_, err := m.client.KillSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), m.token, m.orgID))
		if err != nil {
			return errMsg{err}
		}

		return killedMsg{sandboxID: sandboxID}
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
		if !model.selectedHasSSH {
			return fmt.Errorf("sandbox %s does not have SSH enabled", model.selectedSandboxID)
		}

		// Get SSH connection info via GetSSHConnection, retrying if the
		// sandbox SSH is still being prepared.
		var conn *ssh.SSHConnectionInfo
		maxRetries := 5
		for attempt := 1; attempt <= maxRetries; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			req := &agentv1.GetSSHConnectionRequest{
				SessionId: model.selectedSessionID,
			}
			res, err := client.GetSSHConnection(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
			cancel()

			if err == nil && res.Msg.SshConnection != nil {
				conn = &ssh.SSHConnectionInfo{
					Host:       res.Msg.SshConnection.Host,
					Port:       res.Msg.SshConnection.Port,
					Username:   res.Msg.SshConnection.Username,
					PrivateKey: res.Msg.SshConnection.PrivateKey,
				}
				break
			}

			if attempt == maxRetries {
				if err != nil {
					return fmt.Errorf("unable to get SSH connection info for session %s: %w", model.selectedSessionID, err)
				}
				return fmt.Errorf("no SSH connection available for session %s", model.selectedSessionID)
			}

			time.Sleep(2 * time.Second)
		}

		ssh.PrintConnecting(conn, model.selectedSessionID, "depot sandbox", os.Stdout)
		return ssh.ExecSSH(conn)

	case actionResume:
		enableSSH := model.selectedHasSSH
		timeoutMinutes := int32(60)
		req := &agentv1.StartSandboxRequest{
			ResumeSessionId:      &model.selectedSessionID,
			Argv:                 "",
			EnvironmentVariables: map[string]string{},
			AgentType:            agentv1.AgentType_AGENT_TYPE_UNSPECIFIED,
			SshConfig: &agentv1.SSHConfig{
				Enabled:        enableSSH,
				TimeoutMinutes: &timeoutMinutes,
			},
		}

		spin := newSpinner("Resuming sandbox...", os.Stderr)
		spin.Start()

		ctx := context.Background()
		res, err := client.StartSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
		if err != nil {
			spin.Stop()
			return fmt.Errorf("unable to resume sandbox: %w", err)
		}

		newSessionID := res.Msg.SessionId
		sandboxID := res.Msg.SandboxId

		if !enableSSH {
			spin.Stop()
			fmt.Fprintf(os.Stdout, "\nSandbox resumed!\n")
			fmt.Fprintf(os.Stdout, "Sandbox ID: %s\n", sandboxID)
			fmt.Fprintf(os.Stdout, "Session ID: %s\n", newSessionID)
			fmt.Fprintf(os.Stdout, "\nTo exec:\n  depot sandbox exec %s <command>\n", sandboxID)
			return nil
		}

		// SSH-enabled resume: get connection info
		var conn *ssh.SSHConnectionInfo

		if res.Msg.SshConnection != nil && res.Msg.SshConnection.Host != "" {
			conn = &ssh.SSHConnectionInfo{
				Host:       res.Msg.SshConnection.Host,
				Port:       res.Msg.SshConnection.Port,
				Username:   res.Msg.SshConnection.Username,
				PrivateKey: res.Msg.SshConnection.PrivateKey,
			}
		} else {
			spin.Update("Waiting for sandbox to be ready...")
			conn, err = waitForSSHConnection(ctx, client, token, orgID, newSessionID, sandboxID, false, os.Stderr)
			if err != nil {
				spin.Stop()
				return err
			}
		}

		spin.Stop()
		ssh.PrintConnecting(conn, newSessionID, "depot sandbox", os.Stdout)
		return ssh.ExecSSH(conn)
	}

	return nil
}
