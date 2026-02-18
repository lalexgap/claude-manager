package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"claude-manager/internal/sessions"
	"claude-manager/internal/worktree"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model is the main bubbletea model.
type Model struct {
	allSessions      []sessions.Session
	filteredSessions []sessions.Session
	cursor           int
	search           textinput.Model
	searching        bool
	width            int
	height           int
	showHelp         bool
	chosen          bool // true when user pressed Enter to resume
	newSession      bool // true when user pressed n to start new session
	newSessionPath  string // chosen project path for new session
	showNewSession  bool
	newSessionPaths []projectEntry
	newSessionCursor int
	cwd             string // working directory where claude-manager was launched
	fullTextSearch  bool // true = search all message text, false = summary/project/branch only
	SkipPermissions bool // pass --dangerously-skip-permissions to claude
	UseWorktree     bool // resume in a new git worktree
	showWorktrees   bool
	worktrees       []worktree.Entry
	worktreeCursor  int
	worktreeMsg     string // feedback after removal
}

type projectEntry struct {
	Name string
	Path string
}

// NewModel creates a new TUI model with the given sessions.
func NewModel(ss []sessions.Session, cwd string) Model {
	ti := textinput.New()
	ti.Placeholder = "Search... (@repo to filter by project)"
	ti.CharLimit = 100

	return Model{
		allSessions:      ss,
		filteredSessions: ss,
		search:           ti,
		cwd:              cwd,
	}
}

// Message types for worktree screen
type worktreesLoadedMsg struct {
	entries []worktree.Entry
}

type worktreeRemovedMsg struct {
	idx int
	err error
}

func discoverWorktreesCmd(ss []sessions.Session) tea.Cmd {
	return func() tea.Msg {
		return worktreesLoadedMsg{entries: worktree.Discover(ss)}
	}
}

func removeWorktreeCmd(entries []worktree.Entry, idx int) tea.Cmd {
	return func() tea.Msg {
		err := worktree.Remove(entries[idx])
		return worktreeRemovedMsg{idx: idx, err: err}
	}
}

func (m Model) Init() tea.Cmd {
	return tea.SetWindowTitle("claude-manager")
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case worktreesLoadedMsg:
		m.worktrees = msg.entries
		m.worktreeCursor = 0
		m.worktreeMsg = ""
		return m, nil

	case worktreeRemovedMsg:
		if msg.err != nil {
			m.worktreeMsg = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.worktreeMsg = fmt.Sprintf("Removed %s", m.worktrees[msg.idx].Path)
			m.worktrees = append(m.worktrees[:msg.idx], m.worktrees[msg.idx+1:]...)
			if m.worktreeCursor >= len(m.worktrees) && m.worktreeCursor > 0 {
				m.worktreeCursor--
			}
		}
		return m, nil

	case tea.KeyMsg:
		if m.showNewSession {
			return m.handleNewSessionKey(msg)
		}
		if m.showWorktrees {
			return m.handleWorktreeKey(msg)
		}
		if m.searching {
			return m.handleSearchKey(msg)
		}
		return m.handleNormalKey(msg)
	}

	return m, nil
}

func (m Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searching = false
		m.search.Blur()
		m.search.SetValue("")
		m.applyFilters()
		return m, nil

	case "enter":
		m.searching = false
		m.search.Blur()
		return m, nil

	case "tab":
		m.fullTextSearch = !m.fullTextSearch
		m.applyFilters()
		return m, nil

	default:
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		m.applyFilters()
		return m, cmd
	}
}

func (m Model) handleNormalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "esc":
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}
		if m.search.Value() != "" {
			m.search.SetValue("")
			m.applyFilters()
			return m, nil
		}
		return m, tea.Quit

	case "/":
		m.searching = true
		m.search.Focus()
		return m, textinput.Blink

	case "?":
		m.showHelp = !m.showHelp
		return m, nil

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil

	case "down", "j":
		if m.cursor < len(m.filteredSessions)-1 {
			m.cursor++
		}
		return m, nil

	case "home", "g":
		m.cursor = 0
		return m, nil

	case "end", "G":
		if len(m.filteredSessions) > 0 {
			m.cursor = len(m.filteredSessions) - 1
		}
		return m, nil

	case "pgup":
		m.cursor -= 10
		if m.cursor < 0 {
			m.cursor = 0
		}
		return m, nil

	case "pgdown":
		m.cursor += 10
		if max := len(m.filteredSessions) - 1; m.cursor > max {
			m.cursor = max
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		return m, nil

	case "!":
		m.SkipPermissions = !m.SkipPermissions
		return m, nil

	case "w":
		m.UseWorktree = !m.UseWorktree
		return m, nil

	case "t":
		m.showWorktrees = true
		m.worktreeMsg = ""
		return m, discoverWorktreesCmd(m.allSessions)

	case "n":
		m.showNewSession = true
		m.newSessionCursor = 0
		m.newSessionPaths = m.buildProjectList()
		return m, nil

	case "enter":
		if len(m.filteredSessions) > 0 {
			m.chosen = true
			return m, tea.Quit
		}
		return m, nil
	}

	return m, nil
}

func (m Model) handleWorktreeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.showWorktrees = false
		return m, nil

	case "q", "ctrl+c":
		return m, tea.Quit

	case "up", "k":
		if m.worktreeCursor > 0 {
			m.worktreeCursor--
		}
		return m, nil

	case "down", "j":
		if m.worktreeCursor < len(m.worktrees)-1 {
			m.worktreeCursor++
		}
		return m, nil

	case "d", "x":
		if len(m.worktrees) > 0 && m.worktreeCursor < len(m.worktrees) {
			m.worktreeMsg = fmt.Sprintf("Removing %s...", m.worktrees[m.worktreeCursor].Path)
			return m, removeWorktreeCmd(m.worktrees, m.worktreeCursor)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) buildProjectList() []projectEntry {
	seen := map[string]bool{}
	var entries []projectEntry

	// Current directory first
	if m.cwd != "" {
		entries = append(entries, projectEntry{
			Name: filepath.Base(m.cwd) + " (current dir)",
			Path: m.cwd,
		})
		seen[m.cwd] = true
	}

	// Unique project paths from sessions, ordered by most recent
	for _, s := range m.allSessions {
		if s.ProjectPath == "" || seen[s.ProjectPath] {
			continue
		}
		seen[s.ProjectPath] = true
		entries = append(entries, projectEntry{
			Name: s.Project,
			Path: s.ProjectPath,
		})
	}

	return entries
}

func (m Model) handleNewSessionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.showNewSession = false
		return m, nil

	case "q", "ctrl+c":
		return m, tea.Quit

	case "up", "k":
		if m.newSessionCursor > 0 {
			m.newSessionCursor--
		}
		return m, nil

	case "down", "j":
		if m.newSessionCursor < len(m.newSessionPaths)-1 {
			m.newSessionCursor++
		}
		return m, nil

	case "!":
		m.SkipPermissions = !m.SkipPermissions
		return m, nil

	case "w":
		m.UseWorktree = !m.UseWorktree
		return m, nil

	case "enter":
		if len(m.newSessionPaths) > 0 && m.newSessionCursor < len(m.newSessionPaths) {
			m.newSession = true
			m.newSessionPath = m.newSessionPaths[m.newSessionCursor].Path
			return m, tea.Quit
		}
		return m, nil
	}
	return m, nil
}

func (m Model) renderNewSession() string {
	var b strings.Builder
	b.WriteString(titleStyle.Width(m.width).Render(" claude-manager — New Session"))
	b.WriteString("\n\n")

	if len(m.newSessionPaths) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(1, 2).Render("No projects found"))
		b.WriteString("\n")
	} else {
		listHeight := m.height - 6
		if listHeight < 3 {
			listHeight = 3
		}
		start := 0
		if m.newSessionCursor >= listHeight {
			start = m.newSessionCursor - listHeight + 1
		}
		end := start + listHeight
		if end > len(m.newSessionPaths) {
			end = len(m.newSessionPaths)
		}

		for i := start; i < end; i++ {
			e := m.newSessionPaths[i]
			line := fmt.Sprintf("%s  %s",
				lipgloss.NewStyle().Foreground(highlight).Bold(true).Width(24).Render(e.Name),
				lipgloss.NewStyle().Foreground(dimText).Render(e.Path),
			)
			if i == m.newSessionCursor {
				b.WriteString(selectedItemStyle.Render(line))
			} else {
				b.WriteString(itemStyle.Render(line))
			}
			b.WriteString("\n")
		}
	}

	// Status indicators
	var flags []string
	if m.UseWorktree {
		flags = append(flags, "🌳 worktree")
	}
	if m.SkipPermissions {
		flags = append(flags, "⚡ skip-permissions")
	}
	if len(flags) > 0 {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(0, 2).Render(strings.Join(flags, "  ")))
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("↑↓ navigate • enter select • ! skip-perms • w worktree • Esc back • q quit"))
	return b.String()
}

// parseQuery splits a search query into an optional @project prefix and remaining search text.
// e.g. "@producthunt some query" -> ("producthunt", "some query")
//      "just a query"            -> ("", "just a query")
func parseQuery(raw string) (project, query string) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "@") {
		return "", raw
	}
	// Split on first space after @project
	rest := raw[1:] // strip @
	if idx := strings.IndexByte(rest, ' '); idx >= 0 {
		return rest[:idx], strings.TrimSpace(rest[idx+1:])
	}
	return rest, ""
}

// applyFilters re-applies @project filter + search query.
func (m *Model) applyFilters() {
	project, query := parseQuery(m.search.Value())
	src := m.allSessions
	if project != "" {
		src = filterByProject(src, project)
	}
	m.filteredSessions = filterSessions(src, query, m.fullTextSearch)
	m.cursor = 0
}

// SelectedSession returns the session the user picked via Enter, or nil if they quit.
func (m Model) SelectedSession() *sessions.Session {
	if !m.chosen {
		return nil
	}
	if m.cursor < len(m.filteredSessions) {
		s := m.filteredSessions[m.cursor]
		return &s
	}
	return nil
}

// NewSessionPath returns the project path for the new session, or "" if not chosen.
func (m Model) NewSessionPath() string {
	if !m.newSession {
		return ""
	}
	return m.newSessionPath
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	if m.showNewSession {
		return m.renderNewSession()
	}

	if m.showWorktrees {
		return m.renderWorktrees()
	}

	if m.showHelp {
		return m.renderHelp()
	}

	var b strings.Builder

	// Title bar
	title := titleStyle.Width(m.width).Render(" claude-manager")
	b.WriteString(title)
	b.WriteString("\n")

	// Search bar
	modeTag := ""
	modeTagWidth := 0
	if m.searching || m.search.Value() != "" {
		if m.fullTextSearch {
			modeTag = lipgloss.NewStyle().Foreground(special).Render(" [full-text]")
		} else {
			modeTag = lipgloss.NewStyle().Foreground(dimText).Render(" [quick]")
		}
		modeTagWidth = lipgloss.Width(modeTag)
	}
	searchBoxWidth := m.width - 4 - modeTagWidth
	if searchBoxWidth < 10 {
		searchBoxWidth = 10
	}
	m.search.Width = searchBoxWidth - 4 // account for border + padding
	if m.searching {
		searchBox := searchActiveStyle.Width(searchBoxWidth).Render(m.search.View())
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Center, searchBox, modeTag))
	} else if m.search.Value() != "" {
		searchBox := searchStyle.Width(searchBoxWidth).Render("🔍 " + m.search.Value())
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Center, searchBox, modeTag))
	} else {
		b.WriteString(searchStyle.Width(m.width - 4).Render(lipgloss.NewStyle().Foreground(dimText).Render("/ to search")))
	}
	b.WriteString("\n")

	// Calculate layout
	headerHeight := 4 // title + search + borders
	helpBarHeight := 1
	statusHeight := 1
	detailHeight := 10

	listHeight := m.height - headerHeight - helpBarHeight - statusHeight - detailHeight - 1
	if listHeight < 5 {
		listHeight = 5
		detailHeight = m.height - headerHeight - helpBarHeight - statusHeight - listHeight - 1
	}

	// Session list
	if len(m.filteredSessions) == 0 {
		empty := lipgloss.NewStyle().
			Foreground(dimText).
			Padding(1, 2).
			Render("No sessions found")
		b.WriteString(empty)
	} else {
		// Calculate visible window
		start := 0
		if m.cursor >= listHeight {
			start = m.cursor - listHeight + 1
		}
		end := start + listHeight
		if end > len(m.filteredSessions) {
			end = len(m.filteredSessions)
		}

		for i := start; i < end; i++ {
			selected := i == m.cursor
			b.WriteString(renderSessionItem(m.filteredSessions[i], m.width, selected))
			b.WriteString("\n")
		}

		// Pad remaining lines
		rendered := end - start
		for i := rendered; i < listHeight; i++ {
			b.WriteString("\n")
		}
	}

	// Detail panel
	if len(m.filteredSessions) > 0 && m.cursor < len(m.filteredSessions) && detailHeight > 3 {
		b.WriteString(renderDetail(m.filteredSessions[m.cursor], m.width, detailHeight))
		b.WriteString("\n")
	}

	// Status bar
	status := fmt.Sprintf(" %d sessions", len(m.filteredSessions))
	if len(m.filteredSessions) != len(m.allSessions) {
		status += fmt.Sprintf(" (of %d)", len(m.allSessions))
	}
	if m.UseWorktree {
		status += "  🌳 worktree"
	}
	if m.SkipPermissions {
		status += "  ⚡ skip-permissions"
	}
	b.WriteString(statusBarStyle.Width(m.width).Render(status))
	b.WriteString("\n")

	// Help bar
	help := "↑↓ navigate • enter resume • n new session • w worktree • t worktrees • / search • ! skip-perms • ? help • q quit"
	b.WriteString(helpStyle.Render(help))

	return b.String()
}

func (m Model) renderWorktrees() string {
	var b strings.Builder
	b.WriteString(titleStyle.Width(m.width).Render(" claude-manager — Worktrees"))
	b.WriteString("\n\n")

	if len(m.worktrees) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(1, 2).Render("No worktrees found"))
		b.WriteString("\n")
	} else {
		listHeight := m.height - 6 // title + padding + help + msg
		if listHeight < 3 {
			listHeight = 3
		}
		start := 0
		if m.worktreeCursor >= listHeight {
			start = m.worktreeCursor - listHeight + 1
		}
		end := start + listHeight
		if end > len(m.worktrees) {
			end = len(m.worktrees)
		}

		for i := start; i < end; i++ {
			e := m.worktrees[i]
			repo := filepath.Base(e.RepoRoot)
			line := fmt.Sprintf("%s  %s",
				lipgloss.NewStyle().Foreground(highlight).Bold(true).Width(18).Render(repo),
				lipgloss.NewStyle().Foreground(special).Render(e.Branch),
			)
			if i == m.worktreeCursor {
				b.WriteString(selectedItemStyle.Render(line))
			} else {
				b.WriteString(itemStyle.Render(line))
			}
			b.WriteString("\n")
		}
	}

	if m.worktreeMsg != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(0, 2).Render(m.worktreeMsg))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("↑↓ navigate • d remove • Esc back • q quit"))
	return b.String()
}

func (m Model) renderHelp() string {
	var b strings.Builder
	b.WriteString(titleStyle.Width(m.width).Render(" claude-manager — Help"))
	b.WriteString("\n\n")

	keys := []struct{ key, desc string }{
		{"↑/k", "Move up"},
		{"↓/j", "Move down"},
		{"g/Home", "Go to top"},
		{"G/End", "Go to bottom"},
		{"PgUp/PgDn", "Page up/down"},
		{"Enter", "Resume selected session"},
		{"n", "New session (choose project)"},
		{"w", "Toggle worktree mode"},
		{"t", "Manage worktrees"},
		{"/", "Search (@repo to filter by project)"},
		{"Tab", "Toggle full-text search (in search mode)"},
		{"!", "Toggle --dangerously-skip-permissions"},
		{"Esc", "Clear search / close help"},
		{"?", "Toggle help"},
		{"q", "Quit"},
	}

	for _, k := range keys {
		line := fmt.Sprintf("  %s  %s",
			lipgloss.NewStyle().Foreground(highlight).Bold(true).Width(12).Render(k.key),
			lipgloss.NewStyle().Foreground(white).Render(k.desc),
		)
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("Press ? or Esc to close"))
	return b.String()
}
