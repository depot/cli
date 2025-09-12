package image

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
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	v1 "github.com/depot/cli/pkg/proto/depot/build/v1"
	"github.com/depot/cli/pkg/proto/depot/build/v1/buildv1connect"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdList() *cobra.Command {
	var projectID string
	var token string
	var outputFormat string

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List images in the registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			projectID := helpers.ResolveProjectID(projectID, cwd)
			if projectID == "" {
				return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
			}

			token, err := helpers.ResolveProjectAuth(context.Background(), token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			client := api.NewRegistryClient()

			// Auto-detect CSV output for non-terminal
			if !helpers.IsTerminal() && outputFormat == "" {
				outputFormat = "csv"
			}

			if outputFormat != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				images, err := fetchAllImages(ctx, projectID, token, client)
				if err != nil {
					return err
				}

				if len(images) == 0 {
					fmt.Println("No images found")
					return nil
				}

				switch outputFormat {
				case "csv":
					return images.WriteCSV()
				case "json":
					return images.WriteJSON()
				default:
					return errors.Errorf("unknown format: %s. Requires csv or json", outputFormat)
				}
			}

			// Interactive table view
			columns := []table.Column{
				{Title: "Tag", Width: 50},
				{Title: "Size", Width: 15},
				{Title: "Pushed", Width: 20},
				{Title: "Digest", Width: 30},
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

			m := imagesModel{
				client:      client,
				imagesTable: tbl,
				columns:     columns,
				projectID:   projectID,
				token:       token,
			}

			_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
			return err
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&projectID, "project", "", "Depot project ID")
	flags.StringVar(&token, "token", "", "Depot token")
	flags.StringVar(&outputFormat, "output", "", "Non-interactive output format (json, csv)")

	return cmd
}

type DepotImage struct {
	Tag       string     `json:"tag"`
	Digest    string     `json:"digest"`
	SizeBytes uint64     `json:"size_bytes"`
	PushedAt  *time.Time `json:"pushed_at,omitempty"`
}

type DepotImages []DepotImage

func fetchAllImages(ctx context.Context, projectID, token string, client buildv1connect.RegistryServiceClient) (DepotImages, error) {
	var allImages DepotImages
	var pageToken string

	for {
		req := connect.NewRequest(&v1.ListImagesRequest{
			ProjectId: projectID,
			PageSize:  &[]int32{100}[0],
		})
		if pageToken != "" {
			req.Msg.PageToken = &pageToken
		}

		req = api.WithAuthentication(req, token)
		resp, err := client.ListImages(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("failed to list images: %w", err)
		}

		for _, img := range resp.Msg.Images {
			var pushedAt *time.Time
			if img.PushedAt != nil {
				t := img.PushedAt.AsTime()
				pushedAt = &t
			}
			// The API returns tags in format: registry.depot.dev/PROJECT:TAG
			// We want to show them as PROJECT:TAG
			tag := img.Tag
			if strings.HasPrefix(tag, "registry.depot.dev/") {
				tag = strings.TrimPrefix(tag, "registry.depot.dev/")
			}
			allImages = append(allImages, DepotImage{
				Tag:       tag,
				Digest:    img.Digest,
				SizeBytes: img.SizeBytes,
				PushedAt:  pushedAt,
			})
		}

		if resp.Msg.NextPageToken == nil || *resp.Msg.NextPageToken == "" {
			break
		}
		pageToken = *resp.Msg.NextPageToken
	}

	// Sort images by pushedAt timestamp, newest first
	sort.Slice(allImages, func(i, j int) bool {
		// Handle nil timestamps - put images without timestamps at the end
		if allImages[i].PushedAt == nil && allImages[j].PushedAt == nil {
			return false
		}
		if allImages[i].PushedAt == nil {
			return false
		}
		if allImages[j].PushedAt == nil {
			return true
		}
		// Sort by newest first
		return allImages[i].PushedAt.After(*allImages[j].PushedAt)
	})

	return allImages, nil
}

func (images DepotImages) WriteCSV() error {
	w := csv.NewWriter(os.Stdout)
	if len(images) > 0 {
		if err := w.Write([]string{"Tag", "Digest", "Size (bytes)", "Pushed At"}); err != nil {
			return err
		}
	}

	for _, img := range images {
		var pushedAt string
		if img.PushedAt != nil {
			pushedAt = img.PushedAt.Format(time.RFC3339)
		} else {
			pushedAt = ""
		}

		row := []string{img.Tag, img.Digest, fmt.Sprintf("%d", img.SizeBytes), pushedAt}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}

// WriteJSON outputs images in JSON format
func (images DepotImages) WriteJSON() error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(images)
}

// Bubbletea model for interactive image list
type imagesModel struct {
	client      buildv1connect.RegistryServiceClient
	imagesTable table.Model
	columns     []table.Column
	projectID   string
	token       string
	err         error
}

func (m imagesModel) Init() tea.Cmd {
	return m.loadImages()
}

func (m imagesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			return m, m.loadImages()
		}

	case tea.WindowSizeMsg:
		m.resizeTable(msg)

	case imageRows:
		m.err = nil
		m.imagesTable.SetRows(msg)

	case errMsg:
		m.err = msg.error
	}

	m.imagesTable, cmd = m.imagesTable.Update(msg)
	return m, cmd
}

func (m *imagesModel) resizeTable(msg tea.WindowSizeMsg) {
	h, v := baseStyle.GetFrameSize()
	m.imagesTable.SetHeight(msg.Height - v - 3)
	m.imagesTable.SetWidth(msg.Width - h)

	colWidth := 0
	for _, col := range m.columns {
		colWidth += col.Width
	}

	remainingWidth := msg.Width - colWidth
	if remainingWidth > 0 {
		m.columns[len(m.columns)-1].Width += remainingWidth - h - 4
		m.imagesTable.SetColumns(m.columns)
	}
}

func (m imagesModel) View() string {
	s := baseStyle.Render(m.imagesTable.View()) + "\n"
	if m.err != nil {
		s = "Error: " + m.err.Error() + "\n"
	}
	return s
}

type imageRows []table.Row
type errMsg struct{ error }

func (m imagesModel) loadImages() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		images, err := fetchAllImages(ctx, m.projectID, m.token, m.client)
		if err != nil {
			return errMsg{err}
		}

		rows := []table.Row{}
		for _, img := range images {
			tag := img.Tag
			if len(tag) > 50 {
				tag = tag[:47] + "..."
			}

			size := formatSize(img.SizeBytes)

			var pushedStr string
			if img.PushedAt != nil {
				pushedStr = img.PushedAt.Format(time.RFC3339)
			} else {
				pushedStr = "-"
			}

			digest := img.Digest
			if len(digest) > 30 {
				digest = digest[:27] + "..."
			}

			rows = append(rows, table.Row{tag, size, pushedStr, digest})
		}

		return imageRows(rows)
	}
}

var baseStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("240"))

func formatSize(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
