package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorAccent = lipgloss.Color("212") // pink/magenta
	colorMuted  = lipgloss.Color("240")
	colorError  = lipgloss.Color("196")

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorMuted).
			Padding(0, 1)

	activeBorder = borderStyle.BorderForeground(colorAccent)

	sidebarHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent)

	itemStyle         = lipgloss.NewStyle()
	selectedItemStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle        = lipgloss.NewStyle().Foreground(colorMuted)
	errorStyle        = lipgloss.NewStyle().Foreground(colorError)
)
