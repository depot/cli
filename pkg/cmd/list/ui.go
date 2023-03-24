package list

import (
	"github.com/charmbracelet/lipgloss"
)

// Shared list UI code.

var baseStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("240"))
