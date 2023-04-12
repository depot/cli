// Lists the latests builds for a project.
package list

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdBuilds() *cobra.Command {
	var projectID string
	var token string
	var outputFormat string

	cmd := &cobra.Command{
		Use:     "builds",
		Aliases: []string{"b"},
		Short:   "List builds for a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			projectID := helpers.ResolveProjectID(projectID, cwd)
			if projectID == "" {
				return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
			}

			// TODO: make this a helper
			if token == "" {
				token = os.Getenv("DEPOT_TOKEN")
			}
			if token == "" {
				token = config.GetApiToken()
			}
			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			client := api.NewBuildClient()
			if outputFormat != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				depotBuilds, err := builds(ctx, projectID, token, client)
				if err != nil {
					return err
				}

				switch outputFormat {
				case "csv":
					return writeCSV(depotBuilds)
				case "json":
					return writeJSON(depotBuilds)
				}

				return errors.Errorf("unknown format: %s. Requires csv or json", outputFormat)
			}

			m := newBuildsModel(projectID, token, client)

			_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
			return err
		},
	}

	flags := cmd.Flags()

	flags.StringVar(&projectID, "project", "", "Depot project ID")
	flags.StringVar(&token, "token", "", "Depot API token")
	flags.StringVar(&outputFormat, "output", "", "Non-interactive output format (json, csv)")

	return cmd
}

func newBuildsModel(projectID, token string, client cliv1connect.BuildServiceClient) buildsModel {
	columns := []table.Column{
		{Title: "Build ID", Width: 16},
		{Title: "Status", Width: 16},
		{Title: "Started", Width: 24},
		{Title: "Duration (s)", Width: 16},
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

	m := buildsModel{
		table:     tbl,
		columns:   columns,
		client:    client,
		projectID: projectID,
		token:     token,
	}
	return m
}

type buildsModel struct {
	table   table.Model
	columns []table.Column

	client    cliv1connect.BuildServiceClient
	projectID string
	token     string

	err error
}

func (m buildsModel) Init() tea.Cmd {
	return m.loadBuilds()
}

func (m buildsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC || msg.Type == tea.KeyEsc {
			return m, tea.Quit
		}

		if msg.String() == "q" {
			return m, tea.Quit
		}

		if msg.String() == "r" {
			return m, m.loadBuilds()
		}
	case tea.WindowSizeMsg:
		h, v := baseStyle.GetFrameSize()
		m.table.SetHeight(msg.Height - v - 3)
		m.table.SetWidth(msg.Width - h)

		colWidth := 0
		for _, col := range m.columns {
			colWidth += col.Width
		}

		remainingWidth := msg.Width - colWidth
		m.columns[len(m.columns)-1].Width += remainingWidth - h - 8
		m.table.SetColumns(m.columns)

	case tickMsg:
		return m, m.loadBuilds()
	case buildRows:
		m.err = nil

		var selectedRow table.Row
		if len(m.table.Rows()) > 0 {
			selectedRow = m.table.SelectedRow()
		}

		m.table.SetRows(msg)

		if len(selectedRow) > 0 {
			for i, row := range msg {
				if row[0] == selectedRow[0] {
					m.table.SetCursor(i)
					break
				}
			}
		}

		return m, refreshBuildTimer()
	case errMsg:
		m.err = msg.error
		return m, refreshBuildTimer()
	}
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m buildsModel) View() string {
	s := baseStyle.Render(m.table.View()) + "\n"
	if m.err != nil {
		s = "Error: " + m.err.Error() + "\n"
	}

	return s
}

type buildRows []table.Row
type errMsg struct{ error }

func (m buildsModel) loadBuilds() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		rows := []table.Row{}
		builds, err := builds(ctx, m.projectID, m.token, m.client)
		if err != nil {
			return errMsg{err}
		}

		for _, build := range builds {
			rows = append(rows, table.Row{
				build.ID, build.Status, build.StartTime, fmt.Sprintf("%d", build.Duration),
			})
		}

		return buildRows(rows)
	}
}

type tickMsg struct{}

func refreshBuildTimer() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

type depotBuild struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	StartTime string `json:"startTime"`
	Duration  int    `json:"duration"`
}

func builds(ctx context.Context, projectID, token string, client cliv1connect.BuildServiceClient) ([]depotBuild, error) {
	req := cliv1.ListBuildsRequest{ProjectId: projectID}
	resp, err := client.ListBuilds(ctx, api.WithAuthentication(connect.NewRequest(&req), token))
	if err != nil {
		return nil, err
	}

	res := []depotBuild{}

	for _, build := range resp.Msg.Builds {
		createdAt := build.CreatedAt.AsTime()
		if build.CreatedAt == nil {
			createdAt = time.Now()
		}

		finishedAt := build.FinishedAt.AsTime()
		// This will will cause the duration to increase until the build is complete.
		if build.FinishedAt == nil {
			finishedAt = time.Now()
		}

		startTime := createdAt.Format(time.RFC3339)
		duration := int(finishedAt.Sub(createdAt).Seconds())
		status := strings.ToLower(strings.TrimPrefix(build.Status.String(), "BUILD_STATUS_"))

		res = append(res, depotBuild{
			ID:        build.Id,
			Status:    status,
			StartTime: startTime,
			Duration:  duration,
		})
	}

	return res, nil
}

func writeCSV(depotBuilds []depotBuild) error {
	w := csv.NewWriter(os.Stdout)
	if len(depotBuilds) > 0 {
		if err := w.Write([]string{"Build ID", "Status", "Started", "Duration (s)"}); err != nil {
			return err
		}
	}

	for _, build := range depotBuilds {
		row := []string{build.ID, build.Status, build.StartTime, fmt.Sprintf("%d", build.Duration)}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}

func writeJSON(depotBuilds []depotBuild) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(depotBuilds)
}
