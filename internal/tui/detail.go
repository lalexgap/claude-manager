package tui

import (
	"fmt"
	"strings"

	"claude-manager/internal/sessions"

	"github.com/charmbracelet/lipgloss"
)

// renderDetail renders the detail panel for a session.
func renderDetail(s sessions.Session, width, height int) string {
	if width < 30 {
		return ""
	}

	innerWidth := width - 8 // account for border + padding

	row := func(label, value string) string {
		return fmt.Sprintf("%s %s",
			detailLabelStyle.Render(label),
			detailValueStyle.Render(value),
		)
	}

	headerText := s.Summary
	if s.LLMSummary != "" {
		headerText = s.LLMSummary
	}

	lines := []string{
		lipgloss.NewStyle().Bold(true).Foreground(highlight).Render(headerText),
		"",
		row("Project:", s.Project),
		row("Path:", s.ProjectPath),
		row("Branch:", s.GitBranch),
		row("Last active:", s.LastActive.Local().Format("Jan 2 15:04")+" ("+s.TimeAgo()+")"),
		row("Messages:", fmt.Sprintf("%d", s.MessageCount)),
		row("Session ID:", s.ID),
	}

	if !s.IsWorktree() && s.GitBranch != "" {
		hint := fmt.Sprintf("w: resume in new worktree on branch %s", s.GitBranch)
		lines = append(lines, lipgloss.NewStyle().Foreground(dimText).Italic(true).Render(hint))
	}

	if s.FirstMessage != "" {
		prompt := strings.Join(strings.Fields(s.FirstMessage), " ")
		if len(prompt) > innerWidth*2 {
			prompt = prompt[:innerWidth*2-3] + "..."
		}
		lines = append(lines, "", lipgloss.NewStyle().Foreground(dimText).Render("Initial prompt:"))
		lines = append(lines, lipgloss.NewStyle().Foreground(white).Render("  "+prompt))
	}

	// Inner height = height minus border (2) and padding (2)
	innerHeight := height - 4
	if len(s.LastMessages) > 0 && len(lines) < innerHeight-2 {
		remaining := innerHeight - len(lines) - 2 // reserve 1 for blank + 1 for header
		if remaining > 0 {
			msgs := s.LastMessages
			if len(msgs) > remaining {
				msgs = msgs[len(msgs)-remaining:]
			}
			lines = append(lines, "", lipgloss.NewStyle().Foreground(dimText).Render("Recent context:"))
			for i, msg := range msgs {
				msg = strings.Join(strings.Fields(msg), " ")
				if len(msg) > innerWidth*2 {
					msg = msg[:innerWidth*2-3] + "..."
				}
				prefix := "  · "
				if i == len(msgs)-1 {
					prefix = "  › "
				}
				lines = append(lines, lipgloss.NewStyle().Foreground(white).Render(prefix+msg))
			}
		}
	}

	// Clip to innerHeight to prevent the box from expanding past its allocation
	if len(lines) > innerHeight {
		lines = lines[:innerHeight]
	}

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)

	return detailBorderStyle.
		Width(width - 4).
		Height(height - 4).
		Render(content)
}
