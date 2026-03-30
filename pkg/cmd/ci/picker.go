// picker.go — Interactive fuzzy-finding UI for CI job selection.
//
// This file isolates all TUI/bubbles dependencies behind the PickJob function.
// The rest of the CI command logic should call PickJob without importing any
// bubbletea or lipgloss packages directly. If the TUI library is ever swapped
// out, only this file needs to change.

package ci

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Depot brand green palette.
var (
	depotGreen      = lipgloss.Color("#30a46c")
	depotGreenLight = lipgloss.Color("#3cb179")
	dimColor        = lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#5C5C5C"}
)

// PickJobItem is the data that the picker displays for each selectable job.
type PickJobItem struct {
	Name     string // display name (short or full key)
	Status   string // e.g. "finished", "running"
	Workflow string // workflow path for grouping context
	Index    int    // index back into the caller's slice
}

func (i PickJobItem) FilterValue() string { return i.Name }

// PickJob opens an interactive fuzzy-finding picker and returns the index of
// the selected item, or an error if the user cancelled.
func PickJob(items []PickJobItem) (int, error) {
	listItems := make([]list.Item, len(items))
	for i, item := range items {
		listItems[i] = item
	}

	delegate := newJobDelegate()
	l := list.New(listItems, delegate, 72, min(len(items)+6, 20))
	l.Title = "Select a job"
	l.Styles = depotListStyles()
	l.SetFilteringEnabled(true)
	l.SetShowStatusBar(false)
	l.SetShowHelp(true)
	l.KeyMap.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"))

	m := pickerModel{list: l}
	p := tea.NewProgram(m)

	final, err := p.Run()
	if err != nil {
		return -1, fmt.Errorf("picker error: %w", err)
	}

	fm := final.(pickerModel)
	if fm.cancelled {
		return -1, fmt.Errorf("job selection cancelled")
	}

	selected, ok := fm.list.SelectedItem().(PickJobItem)
	if !ok {
		return -1, fmt.Errorf("no job selected")
	}
	return selected.Index, nil
}

// pickerModel is the bubbletea model that wraps bubbles/list.
type pickerModel struct {
	list      list.Model
	cancelled bool
}

func (m pickerModel) Init() tea.Cmd {
	return nil
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		// Only intercept keys when not actively filtering — otherwise
		// enter/esc need to reach the inner list to confirm/cancel the filter.
		if m.list.FilterState() != list.Filtering {
			switch msg.String() {
			case "enter":
				return m, tea.Quit
			case "esc", "ctrl+c":
				m.cancelled = true
				return m, tea.Quit
			}
		} else if msg.String() == "ctrl+c" {
			// ctrl+c always quits, even mid-filter.
			m.cancelled = true
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m pickerModel) View() string {
	return m.list.View()
}

// jobDelegate is a single-line item delegate that renders each job as:
//
//	job-name (status) — workflow.yml
//
// The job name is prominent, while status and workflow are dimmed.
type jobDelegate struct {
	normalStyle   lipgloss.Style
	selectedStyle lipgloss.Style
	dimStyle      lipgloss.Style
	cursorStyle   lipgloss.Style
	filterMatch   lipgloss.Style
}

func newJobDelegate() jobDelegate {
	return jobDelegate{
		normalStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#dddddd"}).
			Padding(0, 0, 0, 2),
		selectedStyle: lipgloss.NewStyle().
			Foreground(depotGreen).
			Bold(true).
			Padding(0, 0, 0, 1),
		dimStyle: lipgloss.NewStyle().
			Foreground(dimColor),
		cursorStyle: lipgloss.NewStyle().
			Foreground(depotGreen).
			SetString("▸ "),
		filterMatch: lipgloss.NewStyle().
			Foreground(depotGreen).
			Underline(true),
	}
}

func (d jobDelegate) Height() int                             { return 1 }
func (d jobDelegate) Spacing() int                            { return 0 }
func (d jobDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d jobDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	ji, ok := item.(PickJobItem)
	if !ok {
		return
	}

	if m.Width() <= 0 {
		return
	}

	isSelected := index == m.Index()

	// Build the suffix: (status) — workflow.yml
	var suffix string
	if ji.Status != "" {
		suffix += " (" + ji.Status + ")"
	}
	if ji.Workflow != "" {
		suffix += " — " + ji.Workflow
	}

	if isSelected {
		name := d.selectedStyle.Render(ji.Name)
		dim := d.dimStyle.Render(suffix)
		line := d.cursorStyle.String() + name + dim
		line = ansi.Truncate(line, m.Width(), "…")
		fmt.Fprint(w, line)
	} else {
		name := ji.Name
		// Apply filter match highlighting.
		if matchedRunes := m.MatchesForItem(index); len(matchedRunes) > 0 {
			unmatched := d.normalStyle.Inline(true)
			matched := unmatched.Inherit(d.filterMatch)
			name = lipgloss.StyleRunes(name, matchedRunes, matched, unmatched)
			dim := d.dimStyle.Render(suffix)
			line := strings.Repeat(" ", 2) + name + dim
			line = ansi.Truncate(line, m.Width(), "…")
			fmt.Fprint(w, line)
		} else {
			name := d.normalStyle.Render(ji.Name)
			dim := d.dimStyle.Render(suffix)
			line := name + dim
			line = ansi.Truncate(line, m.Width(), "…")
			fmt.Fprint(w, line)
		}
	}
}

// depotListStyles returns list-level styles themed with Depot green.
func depotListStyles() list.Styles {
	s := list.DefaultStyles()

	s.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)

	s.Title = lipgloss.NewStyle().
		Foreground(depotGreen).
		Bold(true)

	s.FilterPrompt = lipgloss.NewStyle().
		Foreground(depotGreen)

	s.FilterCursor = lipgloss.NewStyle().
		Foreground(depotGreenLight)

	s.DefaultFilterCharacterMatch = lipgloss.NewStyle().
		Foreground(depotGreen).
		Underline(true)

	return s
}
