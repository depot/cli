// Lists depot projects.
package list

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1beta1/cliv1beta1connect"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdProjects() *cobra.Command {
	var (
		token        string
		outputFormat string
	)

	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"p"},
		Short:   "List depot projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := helpers.ResolveToken(context.Background(), token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			columns := []table.Column{
				{Title: "Project ID", Width: 24},
				{Title: "Name", Width: 64},
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

			projectClient := api.NewProjectsClient()
			if outputFormat != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				project, err := depotProjects(ctx, token, projectClient)
				if err != nil {
					return err
				}

				switch outputFormat {
				case "csv":
					return projectWriteCSV(project)
				case "json":
					return projectWriteJSON(project)
				}

				return errors.Errorf("unknown format: %s. Requires csv or json", outputFormat)
			}

			m := projectsModel{
				projectClient: projectClient,
				projectsTable: tbl,
				columns:       columns,
				builds:        newBuildsModel("", token, api.NewBuildClient()),
				state:         "projects",
				token:         token,
			}

			_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
			return err
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&token, "token", "", "Depot API token")
	flags.StringVar(&outputFormat, "output", "", "Non-interactive output format (json, csv)")

	return cmd
}

type projectsModel struct {
	projectClient cliv1beta1connect.ProjectsServiceClient
	projectsTable table.Model
	columns       []table.Column

	builds buildsModel
	// Using state to determine if we're in projects or builds
	state string

	token string

	err error
}

func (m projectsModel) Init() tea.Cmd {
	return m.loadProjects()
}

func (m projectsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.state == "builds" {
		if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.Type == tea.KeyEsc {
			// Return to projects
			m.state = "projects"
		} else {
			if update, ok := msg.(tea.WindowSizeMsg); ok {
				m.resizeProjectTable(update)
			}

			buildsTable, cmd := m.builds.Update(msg)
			m.builds = buildsTable.(buildsModel)
			return m, cmd
		}
	}

	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}

		if msg.String() == "q" {
			return m, tea.Quit
		}

		if msg.String() == "r" {
			return m, m.loadProjects()
		}

		if msg.Type == tea.KeyEnter {
			m.state = "builds"
			m.builds.projectID = m.projectsTable.SelectedRow()[0]
			cmd = m.builds.Init()
			return m, cmd
		}

	case tea.WindowSizeMsg:
		m.resizeProjectTable(msg)
		buildsTable, _ := m.builds.Update(msg)
		m.builds = buildsTable.(buildsModel)

	case projects:
		m.err = nil
		m.projectsTable.SetRows(msg)
	case projectErrMsg:
		m.err = msg.error
	}

	m.projectsTable, cmd = m.projectsTable.Update(msg)
	return m, cmd
}

func (m *projectsModel) resizeProjectTable(msg tea.WindowSizeMsg) {
	h, v := baseStyle.GetFrameSize()
	m.projectsTable.SetHeight(msg.Height - v - 3)
	m.projectsTable.SetWidth(msg.Width - h)

	colWidth := 0
	for _, col := range m.columns {
		colWidth += col.Width
	}

	remainingWidth := msg.Width - colWidth
	m.columns[len(m.columns)-1].Width += remainingWidth - h - 4
	m.projectsTable.SetColumns(m.columns)
}

func (m projectsModel) View() string {
	if m.state == "builds" {
		return m.builds.View()
	}

	s := baseStyle.Render(m.projectsTable.View()) + "\n"
	if m.err != nil {
		s = "Error: " + m.err.Error() + "\n"
	}

	return s
}

type projects []table.Row
type projectErrMsg struct{ error }

func (m projectsModel) loadProjects() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		res, err := depotProjects(ctx, m.token, m.projectClient)
		if err != nil {
			return projectErrMsg{err}
		}

		rows := []table.Row{}
		for _, project := range res {
			rows = append(rows, table.Row{project.ID, project.Name})
		}

		return projects(rows)
	}
}

type depotProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func depotProjects(ctx context.Context, token string, client cliv1beta1connect.ProjectsServiceClient) ([]depotProject, error) {
	req := cliv1beta1.ListProjectsRequest{}
	resp, err := client.ListProjects(ctx, api.WithAuthentication(connect.NewRequest(&req), token))
	if err != nil {
		return nil, err
	}
	projects := []depotProject{}
	for _, project := range resp.Msg.Projects {
		projects = append(projects, depotProject{ID: project.Id, Name: project.Name})
	}

	return projects, nil
}

func projectWriteCSV(depotProjects []depotProject) error {
	w := csv.NewWriter(os.Stdout)
	if len(depotProjects) > 0 {
		if err := w.Write([]string{"Project ID", "Name"}); err != nil {
			return err
		}
	}

	for _, project := range depotProjects {
		row := []string{project.ID, project.Name}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}

func projectWriteJSON(depotBuilds []depotProject) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(depotBuilds)
}
