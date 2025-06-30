package org

import (
	"encoding/csv"
	"encoding/json"
	"os"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/pkg/helpers"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

var baseStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("240"))

func NewCmdList() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List organizations that you can access",
		RunE: func(cmd *cobra.Command, args []string) error {
			orgs, err := helpers.RetrieveOrganizations()
			if err != nil {
				return err
			}

			if !helpers.IsTerminal() && outputFormat == "" {
				outputFormat = "csv"
			}

			if outputFormat != "" {
				switch outputFormat {
				case "csv":
					return writeCSV(orgs)
				case "json":
					return writeJSON(orgs)
				default:
					return errors.Errorf("unknown format: %s. Requires csv or json", outputFormat)
				}
			}

			// Interactive table mode
			columns := []table.Column{
				{Title: "Organization ID", Width: 24},
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

			rows := make([]table.Row, len(orgs))
			for i, org := range orgs {
				rows[i] = table.Row{org.OrgId, org.Name}
			}

			t := table.New(
				table.WithColumns(columns),
				table.WithRows(rows),
				table.WithFocused(true),
				table.WithStyles(styles),
			)

			m := model{
				table:   t,
				columns: columns,
			}
			p := tea.NewProgram(m, tea.WithAltScreen())
			_, err = p.Run()
			return err
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&outputFormat, "output", "", "Non-interactive output format (json, csv)")

	return cmd
}

type model struct {
	table   table.Model
	columns []table.Column
	err     error
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
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
		m.columns[len(m.columns)-1].Width += remainingWidth - h - 4
		m.table.SetColumns(m.columns)
	}
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m model) View() string {
	s := baseStyle.Render(m.table.View()) + "\n"
	if m.err != nil {
		s = "Error: " + m.err.Error() + "\n"
	}
	return s
}

func writeCSV(orgs []*helpers.Organization) error {
	w := csv.NewWriter(os.Stdout)
	if len(orgs) > 0 {
		if err := w.Write([]string{"Organization ID", "Name"}); err != nil {
			return err
		}
	}

	for _, org := range orgs {
		row := []string{org.OrgId, org.Name}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}

func writeJSON(orgs []*helpers.Organization) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(orgs)
}
