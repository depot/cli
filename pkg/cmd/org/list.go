package org

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"connectrpc.com/connect"
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

func NewCmdList() *cobra.Command {
	var (
		token        string
		outputFormat string
	)

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all organizations",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token, err := helpers.ResolveToken(ctx, token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

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

			tbl := table.New(
				table.WithColumns(columns),
				table.WithFocused(true),
				table.WithStyles(styles),
			)

			orgClient := api.NewOrganizationClient()
			if !helpers.IsTerminal() && outputFormat == "" {
				outputFormat = "csv"
			}
			if outputFormat != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				orgs, err := depotOrganizations(ctx, token, orgClient)
				if err != nil {
					return err
				}

				switch outputFormat {
				case "csv":
					return orgWriteCSV(orgs)
				case "json":
					return orgWriteJSON(orgs)
				}

				return errors.Errorf("unknown format: %s. Requires csv or json", outputFormat)
			}

			m := organizationsModel{
				orgClient: orgClient,
				orgsTable: tbl,
				columns:   columns,
				token:     token,
			}

			_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
			return err
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&token, "token", "", "Depot token")
	flags.StringVar(&outputFormat, "output", "", "Non-interactive output format (json, csv)")

	return cmd
}

type organizationsModel struct {
	orgClient cliv1beta1connect.OrganizationServiceClient
	orgsTable table.Model
	columns   []table.Column
	token     string
	err       error
}

func (m organizationsModel) Init() tea.Cmd {
	return m.loadOrganizations()
}

func (m organizationsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			return m, m.loadOrganizations()
		}

	case tea.WindowSizeMsg:
		m.resizeOrgTable(msg)

	case organizations:
		m.err = nil
		m.orgsTable.SetRows(msg)
	case orgErrMsg:
		m.err = msg.error
	}

	m.orgsTable, cmd = m.orgsTable.Update(msg)
	return m, cmd
}

func (m *organizationsModel) resizeOrgTable(msg tea.WindowSizeMsg) {
	h, v := baseStyle.GetFrameSize()
	m.orgsTable.SetHeight(msg.Height - v - 3)
	m.orgsTable.SetWidth(msg.Width - h)

	colWidth := 0
	for _, col := range m.columns {
		colWidth += col.Width
	}

	remainingWidth := msg.Width - colWidth
	m.columns[len(m.columns)-1].Width += remainingWidth - h - 4
	m.orgsTable.SetColumns(m.columns)
}

func (m organizationsModel) View() string {
	s := baseStyle.Render(m.orgsTable.View()) + "\n"
	if m.err != nil {
		s = "Error: " + m.err.Error() + "\n"
	}

	return s
}

type organizations []table.Row
type orgErrMsg struct{ error }

func (m organizationsModel) loadOrganizations() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		res, err := depotOrganizations(ctx, m.token, m.orgClient)
		if err != nil {
			return orgErrMsg{err}
		}

		rows := []table.Row{}
		for _, org := range res {
			rows = append(rows, table.Row{org.ID, org.Name})
		}

		return organizations(rows)
	}
}

type depotOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func depotOrganizations(ctx context.Context, token string, client cliv1beta1connect.OrganizationServiceClient) ([]depotOrganization, error) {
	req := cliv1beta1.ListOrganizationRequest{}
	resp, err := client.ListOrganizations(ctx, api.WithAuthentication(connect.NewRequest(&req), token))
	if err != nil {
		return nil, err
	}
	organizations := []depotOrganization{}
	for _, org := range resp.Msg.Organizations {
		organizations = append(organizations, depotOrganization{ID: org.OrgId, Name: org.Name})
	}

	return organizations, nil
}

func orgWriteCSV(depotOrganizations []depotOrganization) error {
	w := csv.NewWriter(os.Stdout)
	if len(depotOrganizations) > 0 {
		if err := w.Write([]string{"Organization ID", "Name"}); err != nil {
			return err
		}
	}

	for _, org := range depotOrganizations {
		row := []string{org.ID, org.Name}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}

func orgWriteJSON(depotOrganizations []depotOrganization) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(depotOrganizations)
}

var baseStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("240"))