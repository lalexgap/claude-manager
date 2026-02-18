package tui

import (
	"fmt"
	"strings"

	"claude-manager/internal/sessions"

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
	chosen           bool // true when user pressed Enter to resume
}

// NewModel creates a new TUI model with the given sessions.
func NewModel(ss []sessions.Session) Model {
	ti := textinput.New()
	ti.Placeholder = "Search sessions..."
	ti.CharLimit = 100

	return Model{
		allSessions:      ss,
		filteredSessions: ss,
		search:           ti,
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

	case tea.KeyMsg:
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
		m.filteredSessions = m.allSessions
		m.cursor = 0
		return m, nil

	case "enter":
		m.searching = false
		m.search.Blur()
		return m, nil

	default:
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		m.filteredSessions = filterSessions(m.allSessions, m.search.Value())
		m.cursor = 0
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
			m.filteredSessions = m.allSessions
			m.cursor = 0
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

	case "enter":
		if len(m.filteredSessions) > 0 {
			m.chosen = true
			return m, tea.Quit
		}
		return m, nil
	}

	return m, nil
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

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
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
	searchWidth := m.width - 4
	if searchWidth < 10 {
		searchWidth = 10
	}
	m.search.Width = searchWidth
	if m.searching {
		b.WriteString(searchActiveStyle.Width(m.width - 4).Render(m.search.View()))
	} else if m.search.Value() != "" {
		b.WriteString(searchStyle.Width(m.width - 4).Render("🔍 " + m.search.Value()))
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
	b.WriteString(statusBarStyle.Width(m.width).Render(status))
	b.WriteString("\n")

	// Help bar
	help := "↑↓ navigate • enter resume • / search • ? help • q quit"
	b.WriteString(helpStyle.Render(help))

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
		{"/", "Search/filter sessions"},
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
