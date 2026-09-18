package list

import "charm.land/lipgloss/v2"

// Shared list UI code.

var baseStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("240"))
