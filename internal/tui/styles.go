package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	subtle    = lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#5C5C5C"}
	highlight = lipgloss.AdaptiveColor{Light: "#874BFD", Dark: "#7D56F4"}
	special   = lipgloss.AdaptiveColor{Light: "#43BF6D", Dark: "#73F59F"}
	dimText   = lipgloss.AdaptiveColor{Light: "#A49FA5", Dark: "#777777"}
	white     = lipgloss.AdaptiveColor{Light: "#1A1A1A", Dark: "#FAFAFA"}

	// Title bar
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	// Search input
	searchStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(subtle).
			Padding(0, 1)

	searchActiveStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(highlight).
				Padding(0, 1)

	// List items
	itemStyle = lipgloss.NewStyle().
			PaddingLeft(2)

	selectedItemStyle = lipgloss.NewStyle().
				PaddingLeft(1).
				Border(lipgloss.ThickBorder(), false, false, false, true).
				BorderForeground(highlight)

	// Item parts
	projectStyle = lipgloss.NewStyle().
			Foreground(highlight).
			Bold(true).
			Width(18)

	summaryStyle = lipgloss.NewStyle().
			Foreground(white)

	branchStyle = lipgloss.NewStyle().
			Foreground(special)

	timeStyle = lipgloss.NewStyle().
			Foreground(dimText)

	// Detail panel
	detailBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(subtle).
				Padding(1, 2)

	detailLabelStyle = lipgloss.NewStyle().
				Foreground(dimText).
				Width(12)

	detailValueStyle = lipgloss.NewStyle().
				Foreground(white)

	// Help bar
	helpStyle = lipgloss.NewStyle().
			Foreground(dimText).
			Padding(0, 1)

	// Status bar
	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#353533")).
			Padding(0, 1)
)
