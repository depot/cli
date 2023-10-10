package init

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bufbuild/connect-go"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/project"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/docker/cli/cli"
	"github.com/spf13/cobra"
)

func NewCmdInit() *cobra.Command {
	var (
		projectID string
		token     string
	)

	cmd := &cobra.Command{
		Use:   "init [flags] [<dir>]",
		Short: "Create a `depot.json` project config",
		Args:  cli.RequiresMaxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			contextDir := "."
			if len(args) > 0 {
				contextDir = args[0]
			}

			absContext, err := filepath.Abs(contextDir)
			if err != nil {
				return err
			}

			_, existingFile, _ := project.ReadConfig(absContext)
			if existingFile != "" && !force {
				return fmt.Errorf("Project configuration %s already exists at path \"%s\", re-run with `--force` to overwrite", filepath.Base(existingFile), filepath.Dir(existingFile))
			}

			token, err = helpers.ResolveToken(context.Background(), token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			client := api.NewProjectsClient()

			req := cliv1beta1.ListProjectsRequest{}
			projects, err := client.ListProjects(context.TODO(), api.WithAuthentication(connect.NewRequest(&req), token))
			if err != nil {
				return err
			}

			if len(projects.Msg.Projects) == 0 {
				fmt.Printf("No projects found. Please create a project first.\n")
				return nil
			}

			if projectID == "" {
				projectID, err = getProjectID(projects.Msg)
				if err != nil {
					return err
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
				return fmt.Errorf("Project with ID %s not found", projectID)
			}

			configFilepath := existingFile
			if configFilepath == "" {
				configFilepath = filepath.Join(absContext, "depot.json")
			}
			err = project.WriteConfig(configFilepath, &project.ProjectConfig{ID: selectedProject.Id})
			if err != nil {
				return err
			}

			fmt.Printf("Project %s (%s) initialized in directory %s\n", selectedProject.Name, selectedProject.OrgName, contextDir)

			return nil
		},
	}

	cmd.Flags().Bool("force", false, "Overwrite any existing project configuration")
	cmd.Flags().StringVar(&projectID, "project", "", "The ID of the project to initialize")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")

	return cmd
}

func getProjectID(projects *cliv1beta1.ListProjectsResponse) (string, error) {
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
