package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"claude-manager/internal/sessions"

	"github.com/charmbracelet/lipgloss"
)

var (
	worktreeIndicator = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C")).Render("wt")
)

// Column widths for the session list.
const (
	colProject = 16
	colBranch  = 24
	colWt      = 3
	colTime    = 8
	colGap     = 5 // spaces between columns
)

// renderSessionItem renders a single session row.
// Layout: project(16) branch(24) wt(3) time(8) summary(remaining)
// semanticScore is optional — if > 0, it's shown as a percentage.
func renderSessionItem(s sessions.Session, width int, selected bool, semanticScore ...float64) string {
	project := projectStyle.Width(colProject).Render(truncate(s.Project, colProject))

	branch := branchStyle.Width(colBranch).Render(truncate(s.GitBranch, colBranch))

	wt := ""
	if s.IsWorktree() {
		wt = worktreeIndicator
	}
	wtCol := lipgloss.NewStyle().Width(colWt).Render(wt)

	timeStr := s.TimeAgo()
	if len(semanticScore) > 0 && semanticScore[0] > 0 {
		timeStr = fmt.Sprintf("%d%%", int(semanticScore[0]*100))
	}
	timeCol := timeStyle.Width(colTime).Render(timeStr)

	fixedWidth := colProject + colBranch + colWt + colTime + colGap
	summaryWidth := width - fixedWidth - 4 // padding
	if summaryWidth < 10 {
		summaryWidth = 10
	}

	displaySummary := s.Summary
	if s.LLMSummary != "" {
		displaySummary = s.LLMSummary
	}
	summary := summaryStyle.Render(truncate(displaySummary, summaryWidth))

	line := fmt.Sprintf("%s %s %s %s %s", project, branch, wtCol, timeCol, summary)

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

// filterByCwd returns sessions whose ProjectPath is cwd itself or lives underneath it.
// Paths are compared after filepath.Clean to avoid trailing-separator mismatches.
func filterByCwd(all []sessions.Session, cwd string) []sessions.Session {
	if cwd == "" {
		return all
	}
	var result []sessions.Session
	for _, s := range all {
		if sessionInCwd(s, cwd) {
			result = append(result, s)
		}
	}
	return result
}

// sessionInCwd reports whether the session was started at cwd or in a subdirectory.
func sessionInCwd(s sessions.Session, cwd string) bool {
	if cwd == "" || s.ProjectPath == "" {
		return false
	}
	root := filepath.Clean(cwd)
	p := filepath.Clean(s.ProjectPath)
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(os.PathSeparator))
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
