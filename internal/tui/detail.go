package tui

import (
	"fmt"

	"claude-manager/internal/sessions"

	"github.com/charmbracelet/lipgloss"
)

// renderDetail renders the detail panel for a session.
func renderDetail(s sessions.Session, width, height int) string {
	if width < 30 {
		return ""
	}

	row := func(label, value string) string {
		return fmt.Sprintf("%s %s",
			detailLabelStyle.Render(label),
			detailValueStyle.Render(value),
		)
	}

	lines := []string{
		lipgloss.NewStyle().Bold(true).Foreground(highlight).Render(s.Summary),
		"",
		row("Project:", s.Project),
		row("Path:", s.ProjectPath),
		row("Branch:", s.GitBranch),
		row("Last active:", s.LastActive.Local().Format("Jan 2 15:04") + " (" + s.TimeAgo() + ")"),
		row("Messages:", fmt.Sprintf("%d", s.MessageCount)),
		row("Session ID:", s.ID),
	}

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)

	return detailBorderStyle.
		Width(width - 4).
		Height(height - 4).
		Render(content)
}
