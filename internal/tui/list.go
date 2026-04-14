package tui

import (
	"fmt"
	"strings"

	"claude-manager/internal/sessions"

	"github.com/charmbracelet/lipgloss"
)

// renderSessionItem renders a single session row.
// semanticScore is optional — if > 0, it's shown as a percentage.
func renderSessionItem(s sessions.Session, width int, selected bool, semanticScore ...float64) string {
	project := projectStyle.Render(truncate(s.Project, 16))

	branch := ""
	if s.GitBranch != "" {
		branch = branchStyle.Render(truncate(s.GitBranch, 30))
	}

	timeAgo := timeStyle.Render(s.TimeAgo())

	score := ""
	if len(semanticScore) > 0 && semanticScore[0] > 0 {
		pct := int(semanticScore[0] * 100)
		score = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF79C6")).Render(fmt.Sprintf(" %d%%", pct))
	}

	// Calculate remaining width for summary
	// project(18) + branch(~32) + time(~10) + padding(~8)
	rightSide := fmt.Sprintf(" %s  %s%s", branch, timeAgo, score)
	summaryWidth := width - 18 - lipgloss.Width(rightSide) - 6
	if summaryWidth < 20 {
		summaryWidth = 20
	}

	displaySummary := s.Summary
	if s.LLMSummary != "" {
		displaySummary = s.LLMSummary
	}
	summary := summaryStyle.Render(truncate(displaySummary, summaryWidth))

	line := fmt.Sprintf("%s %s%s", project, summary, rightSide)

	if selected {
		return selectedItemStyle.Render(line)
	}
	return itemStyle.Render(line)
}

// filterSessions returns sessions matching the query (case-insensitive substring match).
// When fullText is true, also searches all user message text.
func filterSessions(all []sessions.Session, query string, fullText bool) []sessions.Session {
	if query == "" {
		return all
	}
	q := strings.ToLower(query)
	var result []sessions.Session
	for _, s := range all {
		if strings.Contains(strings.ToLower(s.Summary), q) ||
			strings.Contains(strings.ToLower(s.Project), q) ||
			strings.Contains(strings.ToLower(s.GitBranch), q) {
			result = append(result, s)
		} else if fullText && strings.Contains(strings.ToLower(s.MessageText), q) {
			result = append(result, s)
		}
	}
	return result
}

// filterByProject returns sessions whose project name contains the given substring.
func filterByProject(all []sessions.Session, project string) []sessions.Session {
	p := strings.ToLower(project)
	var result []sessions.Session
	for _, s := range all {
		if strings.Contains(strings.ToLower(s.Project), p) {
			result = append(result, s)
		}
	}
	return result
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
