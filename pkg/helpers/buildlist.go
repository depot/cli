package helpers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/pkg/api"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/pkg/errors"
)

func SelectBuildID(ctx context.Context, token, projectID string, client cliv1connect.BuildServiceClient) (string, error) {
	m := NewBuildsModel(projectID, token, client)

	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return "", err
	}

	m, ok := final.(BuildsModel)
	if !ok {
		return "", errors.Errorf("invalid model: %T", final)
	}

	return m.SelectedBuildID, nil
}

func NewBuildsModel(projectID, token string, client cliv1connect.BuildServiceClient) BuildsModel {
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

	m := BuildsModel{
		table:     tbl,
		columns:   columns,
		client:    client,
		ProjectID: projectID,
		Token:     token,
	}
	return m
}

type BuildsModel struct {
	table   table.Model
	columns []table.Column

	client cliv1connect.BuildServiceClient

	ProjectID       string
	Token           string
	SelectedBuildID string

	err error
}

func (m BuildsModel) Init() tea.Cmd {
	return m.loadBuilds()
}

func (m BuildsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

		if msg.String() == "enter" {
			if len(m.table.Rows()) == 0 {
				return m, nil
			}

			row := m.table.SelectedRow()
			m.SelectedBuildID = row[0]
			return m, tea.Quit
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

func (m BuildsModel) View() string {
	s := baseStyle.Render(m.table.View()) + "\n"
	if m.err != nil {
		s = "Error: " + m.err.Error() + "\n"
	}

	return s
}

type buildRows []table.Row
type errMsg struct{ error }

func (m BuildsModel) loadBuilds() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		rows := []table.Row{}
		builds, err := builds(ctx, m.ProjectID, m.Token, m.client)
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

var baseStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("240"))
