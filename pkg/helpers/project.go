package helpers

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bufbuild/connect-go"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/project"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/sirupsen/logrus"
)

// Returns the project ID from the environment or config file.
// Searches from the directory of each of the files.
func ResolveProjectID(id string, files ...string) string {
	if id != "" {
		return id
	}

	id = os.Getenv("DEPOT_PROJECT_ID")
	if id != "" {
		return id
	}

	dirs, err := WorkingDirectories(files...)
	if err != nil {
		return ""
	}

	// Only a single project ID is allowed.
	uniqueIDs := make(map[string]struct{})

	for _, dir := range dirs {
		cwd, _ := filepath.Abs(dir)
		config, _, err := project.ReadConfig(cwd)
		if err == nil {
			id = config.ID
			uniqueIDs[id] = struct{}{}
		}
	}

	// TODO: Warn for multiple project IDs. Is this an error?
	if len(uniqueIDs) > 1 {
		ids := []string{}
		for id := range uniqueIDs {
			ids = append(ids, id)
		}

		logrus.Warnf("More than one project ID discovered: %s.  Using project: %s", strings.Join(ids, ", "), id)
	}

	return id
}

// Returns all directories for any files.  If no files are specified then
// the current working directory is returned.  Special handling for stdin
// is also included by assuming the current working directory.
func WorkingDirectories(files ...string) ([]string, error) {
	directories := []string{}
	if len(files) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		directories = append(directories, cwd)
	}

	for _, file := range files {
		if file == "-" || file == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			directories = append(directories, cwd)
			continue
		}

		if fi, err := os.Stat(file); err == nil && fi.IsDir() {
			directories = append(directories, file)
		} else {
			directories = append(directories, filepath.Dir(file))
		}
	}

	return directories, nil
}

type SelectedProject struct {
	OrgName string
	Name    string
	ID      string
}

func InitializeProject(ctx context.Context, token, projectID string) (*SelectedProject, error) {
	client := api.NewProjectsClient()

	req := cliv1beta1.ListProjectsRequest{}
	projects, err := client.ListProjects(ctx, api.WithAuthentication(connect.NewRequest(&req), token))
	if err != nil {
		return nil, err
	}

	if len(projects.Msg.Projects) == 0 {
		return nil, fmt.Errorf("No projects found. Please create a project first.")
	}

	if !IsTerminal() {
		if len(projects.Msg.Projects) > 0 {
			fmt.Printf("Available Projects\n")
			fmt.Printf("------------------\n\n")

			w := csv.NewWriter(os.Stdout)
			if err := w.Write([]string{"Project ID", "Name"}); err != nil {
				return nil, err
			}
			for _, project := range projects.Msg.GetProjects() {
				row := []string{project.Id, project.Name}
				if err := w.Write(row); err != nil {
					return nil, err
				}
			}
			w.Flush()
			_ = w.Error()
			fmt.Printf("\n\n")
		}
		return nil, fmt.Errorf("missing project ID; please run `depot init` or `depot build --project <id>`")
	}

	if projectID == "" {
		projectID, err = GetProjectID(projects.Msg)
		if err != nil {
			return nil, fmt.Errorf("No project selected; please run `depot init`")
		}
	}

	var selectedProject *cliv1beta1.ListProjectsResponse_Project
	for _, p := range projects.Msg.Projects {
		if p.Id == projectID {
			selectedProject = p
			break
		}
	}

	if selectedProject == nil {
		return nil, fmt.Errorf("Project with ID %s not found", projectID)
	}

	return &SelectedProject{
		OrgName: selectedProject.OrgName,
		Name:    selectedProject.Name,
		ID:      selectedProject.Id,
	}, nil
}

func GetProjectID(projects *cliv1beta1.ListProjectsResponse) (string, error) {
	items := []list.Item{}
	for _, p := range projects.Projects {
		items = append(items, item{id: p.Id, title: p.Name, desc: p.OrgName})
	}

	m := model{list: list.New(items, list.NewDefaultDelegate(), 0, 0), ctrlC: false}
	m.list.Title = "Choose a project"

	p := tea.NewProgram(m, tea.WithAltScreen())

	final, err := p.Run()
	if err != nil {
		fmt.Println("Error running program:", err)
		os.Exit(1)
	}
	m, ok := final.(model)
	if !ok {
		return "", fmt.Errorf("final model is not the expected type")
	}

	if m.ctrlC {
		os.Exit(1)
	}

	if m.choice == nil {
		return "", fmt.Errorf("No project selected")
	}

	return m.choice.id, nil
}

var docStyle = lipgloss.NewStyle().Margin(1, 2)

type item struct {
	id, title, desc string
}

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.desc }
func (i item) FilterValue() string { return i.title }

type model struct {
	list   list.Model
	choice *item
	ctrlC  bool
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.ctrlC = true
			return m, tea.Quit
		}

		if msg.String() == "enter" {
			if i, ok := m.list.SelectedItem().(item); ok {
				m.choice = &i
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

func (m model) View() string {
	return docStyle.Render(m.list.View())
}
