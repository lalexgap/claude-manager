package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lalexgap/claude-manager/internal/orchestrator"
	"github.com/lalexgap/claude-manager/internal/sessions"

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
	chosen           bool   // true when user pressed Enter to resume
	newSession       bool   // true when user pressed n to start new session
	newSessionPath   string // chosen project path for new session
	showNewSession   bool
	newSessionPaths  []projectEntry
	newSessionCursor int
	cwd              string // working directory where claude-manager was launched
	fullTextSearch   bool   // true = search all message text, false = summary/project/branch only
	SkipPermissions  bool   // pass --dangerously-skip-permissions to claude
	UseWorktree      bool   // pass --worktree to claude for resume/new session
	showWorktrees    bool   // shows built-in worktree info
	showOrchestrator bool

	orchestratorStore      *orchestrator.Store
	orchestratorStoreReady bool
	orchestratorMainEnv    orchestrator.MainEnvStatus
	orchestratorAgents     []orchestrator.Agent
	orchestratorQueue      []orchestrator.MainEnvRequest
	orchestratorCursor     int
	orchestratorStatus     string
	orchestratorAdding     bool
	orchestratorAddFocus   int
	orchestratorAddInputs  []textinput.Model
}

const (
	orchestratorAddFieldName = iota
	orchestratorAddFieldWorkspace
	orchestratorAddFieldCount
)

type orchestratorAttachDoneMsg struct {
	agentName string
	err       error
}

type orchestratorInitMsg struct{}

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
		UseWorktree:      true, // always run sessions in a worktree
		showOrchestrator: true, // orchestrator is now the primary screen
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tea.SetWindowTitle("claude-manager"),
		func() tea.Msg { return orchestratorInitMsg{} },
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.showOrchestrator {
			return m.handleOrchestratorKey(msg)
		}
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

	case orchestratorAttachDoneMsg:
		if msg.err != nil {
			m.orchestratorStatus = fmt.Sprintf("Attach failed for %q: %v", msg.agentName, msg.err)
			return m, nil
		}
		if err := m.refreshOrchestratorData(); err != nil {
			m.orchestratorStatus = fmt.Sprintf("Detached from %q; refresh failed: %v", msg.agentName, err)
			return m, nil
		}
		m.orchestratorStatus = fmt.Sprintf("Detached from %q.", msg.agentName)
		return m, nil

	case orchestratorInitMsg:
		if m.showOrchestrator {
			if err := m.refreshOrchestratorData(); err != nil {
				m.orchestratorStatus = fmt.Sprintf("Orchestrator load failed: %v", err)
			} else {
				m.orchestratorStatus = "Orchestrator ready."
			}
		}
		return m, nil
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
		if m.showOrchestrator {
			m.showOrchestrator = false
			return m, nil
		}
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

	case "w", "t":
		m.showWorktrees = true
		return m, nil

	case "o":
		m.showOrchestrator = true
		if err := m.refreshOrchestratorData(); err != nil {
			m.orchestratorStatus = fmt.Sprintf("Orchestrator load failed: %v", err)
		} else {
			m.orchestratorStatus = "Orchestrator view refreshed."
		}
		return m, nil

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
	}
	return m, nil
}

func (m Model) handleOrchestratorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.orchestratorAdding {
		return m.handleOrchestratorAddKey(msg)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		return m, nil
	case "r":
		if err := m.refreshOrchestratorData(); err != nil {
			m.orchestratorStatus = fmt.Sprintf("Refresh failed: %v", err)
		} else {
			m.orchestratorStatus = "Refreshed orchestrator state."
		}
		return m, nil
	case "a":
		m = m.openOrchestratorAddForm()
		return m, textinput.Blink
	case "up", "k":
		if m.orchestratorCursor > 0 {
			m.orchestratorCursor--
		}
		return m, nil
	case "down", "j":
		if m.orchestratorCursor < len(m.orchestratorAgents)-1 {
			m.orchestratorCursor++
		}
		return m, nil
	case "home", "g":
		m.orchestratorCursor = 0
		return m, nil
	case "end", "G":
		if len(m.orchestratorAgents) > 0 {
			m.orchestratorCursor = len(m.orchestratorAgents) - 1
		}
		return m, nil
	case "enter", "v":
		return m.runOrchestratorAttachSelected()
	case "s":
		return m.runOrchestratorStartSelected(), nil
	case "S":
		return m.runOrchestratorStartAllAgents(), nil
	case "x":
		return m.runOrchestratorStopSelected(false), nil
	case "X":
		return m.runOrchestratorStopSelected(true), nil
	case "h":
		return m.runOrchestratorHeartbeatSelected(), nil
	case "f":
		return m.runOrchestratorQueueRunFeatureSpecs(), nil
	case "d":
		return m.runOrchestratorQueueDevServer("status"), nil
	case "D":
		return m.runOrchestratorQueueDevServer("start"), nil
	case "n":
		return m.runOrchestratorGrantNext(), nil
	case "R":
		return m.runOrchestratorRunNext(), nil
	case "u":
		return m.runOrchestratorRenewLease(), nil
	case "l":
		return m.runOrchestratorReleaseSuccess(), nil
	}
	return m, nil
}

func (m Model) handleOrchestratorAddKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.orchestratorAdding = false
		m.orchestratorAddInputs = nil
		return m, nil
	case "tab":
		m = m.setOrchestratorAddFocus((m.orchestratorAddFocus + 1) % orchestratorAddFieldCount)
		return m, nil
	case "shift+tab", "backtab":
		next := m.orchestratorAddFocus - 1
		if next < 0 {
			next = orchestratorAddFieldCount - 1
		}
		m = m.setOrchestratorAddFocus(next)
		return m, nil
	case "enter":
		return m.runOrchestratorAddAgent(), nil
	default:
		if len(m.orchestratorAddInputs) != orchestratorAddFieldCount {
			return m, nil
		}
		var cmd tea.Cmd
		m.orchestratorAddInputs[m.orchestratorAddFocus], cmd = m.orchestratorAddInputs[m.orchestratorAddFocus].Update(msg)
		return m, cmd
	}
}

func (m Model) openOrchestratorAddForm() Model {
	workspace := m.defaultOrchestratorWorkspace()
	defaultName := defaultOrchestratorAgentName(workspace)

	nameInput := textinput.New()
	nameInput.Prompt = ""
	nameInput.Placeholder = "agent name"
	nameInput.CharLimit = 80
	nameInput.SetValue(defaultName)

	workspaceInput := textinput.New()
	workspaceInput.Prompt = ""
	workspaceInput.Placeholder = "workspace path"
	workspaceInput.CharLimit = 512
	workspaceInput.SetValue(workspace)

	m.orchestratorAddInputs = []textinput.Model{nameInput, workspaceInput}
	m.orchestratorAdding = true
	m = m.setOrchestratorAddFocus(orchestratorAddFieldName)
	return m
}

func (m Model) setOrchestratorAddFocus(idx int) Model {
	if len(m.orchestratorAddInputs) != orchestratorAddFieldCount {
		return m
	}
	if idx < 0 || idx >= orchestratorAddFieldCount {
		idx = 0
	}
	m.orchestratorAddFocus = idx
	for i := range m.orchestratorAddInputs {
		if i == idx {
			m.orchestratorAddInputs[i].Focus()
			continue
		}
		m.orchestratorAddInputs[i].Blur()
	}
	return m
}

func (m Model) runOrchestratorAddAgent() Model {
	if len(m.orchestratorAddInputs) != orchestratorAddFieldCount {
		m.orchestratorStatus = "Add Agent form is unavailable."
		m.orchestratorAdding = false
		return m
	}

	name := strings.TrimSpace(m.orchestratorAddInputs[orchestratorAddFieldName].Value())
	workspace := strings.TrimSpace(m.orchestratorAddInputs[orchestratorAddFieldWorkspace].Value())

	var added orchestrator.Agent
	err := m.withOrchestratorStore(func(store *orchestrator.Store) error {
		var err error
		added, err = store.AddAgent(name, workspace)
		return err
	})
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Add agent failed: %v", err)
		return m
	}

	m.orchestratorAdding = false
	m.orchestratorAddInputs = nil
	if err := m.refreshOrchestratorData(); err != nil {
		m.orchestratorStatus = fmt.Sprintf("Added %q; refresh failed: %v", added.Name, err)
		return m
	}
	m.orchestratorStatus = fmt.Sprintf("Added agent %q (%s).", added.Name, added.WorkspacePath)
	return m
}

func (m Model) defaultOrchestratorWorkspace() string {
	if m.cursor >= 0 && m.cursor < len(m.filteredSessions) {
		if path := strings.TrimSpace(m.filteredSessions[m.cursor].ProjectPath); path != "" {
			return path
		}
	}
	if path := strings.TrimSpace(m.cwd); path != "" {
		return path
	}
	return ""
}

func defaultOrchestratorAgentName(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return ""
	}
	base := filepath.Base(workspace)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	cleanBase := sanitizeOrchestratorAgentNamePart(base)
	if cleanBase == "" {
		return ""
	}
	return cleanBase + "-agent"
}

func sanitizeOrchestratorAgentNamePart(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}

	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		isLetter := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if isLetter || isDigit {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}

	return strings.Trim(b.String(), "-")
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

	case "w", "t":
		m.showWorktrees = true
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
		flags = append(flags, "🌳 worktree (always)")
	}
	if m.SkipPermissions {
		flags = append(flags, "⚡ skip-permissions")
	}
	if len(flags) > 0 {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(0, 2).Render(strings.Join(flags, "  ")))
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("↑↓ navigate • enter select • ! skip-perms • t worktree info • Esc back • q quit"))
	return b.String()
}

// parseQuery splits a search query into an optional @project prefix and remaining search text.
// e.g. "@producthunt some query" -> ("producthunt", "some query")
//
//	"just a query"            -> ("", "just a query")
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

	if m.showOrchestrator {
		return m.renderOrchestrator()
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
		status += "  🌳 worktree (always)"
	}
	if m.SkipPermissions {
		status += "  ⚡ skip-permissions"
	}
	b.WriteString(statusBarStyle.Width(m.width).Render(status))
	b.WriteString("\n")

	// Help bar
	help := "↑↓ navigate • enter resume • n new session • o orchestrator • t worktree info • / search • ! skip-perms • ? help • q quit"
	b.WriteString(helpStyle.Render(help))

	return b.String()
}

func (m *Model) ensureOrchestratorStore() error {
	if m.orchestratorStore == nil {
		store, err := orchestrator.NewStore()
		if err != nil {
			return err
		}
		m.orchestratorStore = store
	}
	if m.orchestratorStoreReady {
		return nil
	}

	status, err := m.orchestratorStore.Status()
	if err != nil {
		return err
	}
	if !status.Initialized {
		if err := m.orchestratorStore.Init(); err != nil {
			return err
		}
	}

	m.orchestratorStoreReady = true
	return nil
}

func (m *Model) withOrchestratorStore(run func(*orchestrator.Store) error) error {
	if err := m.ensureOrchestratorStore(); err != nil {
		return err
	}

	err := run(m.orchestratorStore)
	if errors.Is(err, orchestrator.ErrNotInitialized) {
		m.orchestratorStoreReady = false
		if reinitErr := m.ensureOrchestratorStore(); reinitErr != nil {
			return reinitErr
		}
		return run(m.orchestratorStore)
	}
	return err
}

func (m *Model) refreshOrchestratorData() error {
	var mainEnv orchestrator.MainEnvStatus
	var agents []orchestrator.Agent
	var queue []orchestrator.MainEnvRequest

	err := m.withOrchestratorStore(func(store *orchestrator.Store) error {
		var err error
		mainEnv, err = store.MainEnvStatus()
		if err != nil {
			return err
		}
		agents, err = store.ListAgents()
		if err != nil {
			return err
		}
		queue, err = store.ListMainEnvQueue()
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	m.orchestratorMainEnv = mainEnv
	m.orchestratorAgents = agents
	m.orchestratorQueue = queue
	if m.orchestratorCursor >= len(m.orchestratorAgents) {
		m.orchestratorCursor = len(m.orchestratorAgents) - 1
	}
	if m.orchestratorCursor < 0 {
		m.orchestratorCursor = 0
	}
	return nil
}

func (m Model) selectedOrchestratorAgent() (orchestrator.Agent, bool) {
	if len(m.orchestratorAgents) == 0 {
		return orchestrator.Agent{}, false
	}
	if m.orchestratorCursor < 0 || m.orchestratorCursor >= len(m.orchestratorAgents) {
		return orchestrator.Agent{}, false
	}
	return m.orchestratorAgents[m.orchestratorCursor], true
}

func (m Model) runOrchestratorAttachSelected() (Model, tea.Cmd) {
	agent, ok := m.selectedOrchestratorAgent()
	if !ok {
		m.orchestratorStatus = "No agent selected."
		return m, nil
	}

	tmuxName, isTmux := orchestrator.ParseAgentTmuxSessionID(agent.SessionID)
	if !isTmux {
		m.orchestratorStatus = fmt.Sprintf("Agent %q is not running in tmux. Start it with s/S first.", agent.Name)
		return m, nil
	}

	m.orchestratorStatus = fmt.Sprintf("Attaching to %q (%s). Detach with Ctrl+b then d.", agent.Name, tmuxName)
	cmd := exec.Command("tmux", "attach-session", "-t", tmuxName)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return orchestratorAttachDoneMsg{agentName: agent.Name, err: err}
	})
}

func (m Model) runOrchestratorStartSelected() Model {
	agent, ok := m.selectedOrchestratorAgent()
	if !ok {
		m.orchestratorStatus = "No agent selected."
		return m
	}

	var started orchestrator.Agent
	err := m.withOrchestratorStore(func(store *orchestrator.Store) error {
		var err error
		started, err = store.StartAgentTmux(agent.Name, []string{"claude"})
		return err
	})
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Start failed for %q: %v", agent.Name, err)
		return m
	}
	if err := m.refreshOrchestratorData(); err != nil {
		m.orchestratorStatus = fmt.Sprintf("Started %q (%s); refresh failed: %v", started.Name, started.SessionID, err)
		return m
	}
	m.orchestratorStatus = fmt.Sprintf("Started %q (%s).", started.Name, started.SessionID)
	return m
}

func (m Model) runOrchestratorStartAllAgents() Model {
	if len(m.orchestratorAgents) == 0 {
		m.orchestratorStatus = "No agents found to start."
		return m
	}

	started := 0
	skippedRunning := 0
	failed := 0
	failureSamples := make([]string, 0, 2)

	err := m.withOrchestratorStore(func(store *orchestrator.Store) error {
		for _, agent := range m.orchestratorAgents {
			current, alive, err := store.AgentStatus(agent.Name)
			if err != nil {
				failed++
				if len(failureSamples) < 2 {
					failureSamples = append(failureSamples, fmt.Sprintf("%s: %v", agent.Name, err))
				}
				continue
			}
			if alive {
				if _, hasPID := orchestrator.ParseAgentPIDSessionID(current.SessionID); hasPID {
					skippedRunning++
					continue
				}
				if _, hasTmux := orchestrator.ParseAgentTmuxSessionID(current.SessionID); hasTmux {
					skippedRunning++
					continue
				}
			}
			if _, err := store.StartAgentTmux(agent.Name, []string{"claude"}); err != nil {
				failed++
				if len(failureSamples) < 2 {
					failureSamples = append(failureSamples, fmt.Sprintf("%s: %v", agent.Name, err))
				}
				continue
			}
			started++
		}
		return nil
	})
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Batch start failed: %v", err)
		return m
	}

	if err := m.refreshOrchestratorData(); err != nil {
		m.orchestratorStatus = fmt.Sprintf("Batch start done (started=%d, skipped=%d, failed=%d); refresh failed: %v", started, skippedRunning, failed, err)
		return m
	}

	m.orchestratorStatus = fmt.Sprintf("Batch start: started=%d skipped-running=%d failed=%d", started, skippedRunning, failed)
	if len(failureSamples) > 0 {
		m.orchestratorStatus += " [" + strings.Join(failureSamples, " | ") + "]"
	}
	return m
}

func (m Model) runOrchestratorStopSelected(force bool) Model {
	agent, ok := m.selectedOrchestratorAgent()
	if !ok {
		m.orchestratorStatus = "No agent selected."
		return m
	}

	var stopped orchestrator.Agent
	err := m.withOrchestratorStore(func(store *orchestrator.Store) error {
		var err error
		stopped, err = store.StopAgentProcess(agent.Name, force)
		return err
	})
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Stop failed for %q: %v", agent.Name, err)
		return m
	}
	if err := m.refreshOrchestratorData(); err != nil {
		m.orchestratorStatus = fmt.Sprintf("Stopped %q; refresh failed: %v", stopped.Name, err)
		return m
	}
	if force {
		m.orchestratorStatus = fmt.Sprintf("Force-stopped %q.", stopped.Name)
		return m
	}
	m.orchestratorStatus = fmt.Sprintf("Stopped %q.", stopped.Name)
	return m
}

func (m Model) runOrchestratorHeartbeatSelected() Model {
	agent, ok := m.selectedOrchestratorAgent()
	if !ok {
		m.orchestratorStatus = "No agent selected."
		return m
	}

	var updated orchestrator.Agent
	err := m.withOrchestratorStore(func(store *orchestrator.Store) error {
		var err error
		updated, err = store.HeartbeatAgent(agent.Name)
		return err
	})
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Heartbeat failed for %q: %v", agent.Name, err)
		return m
	}
	if err := m.refreshOrchestratorData(); err != nil {
		m.orchestratorStatus = fmt.Sprintf("Heartbeat updated for %q; refresh failed: %v", updated.Name, err)
		return m
	}
	m.orchestratorStatus = fmt.Sprintf("Heartbeat updated for %q at %s.", updated.Name, updated.LastHeartbeat)
	return m
}

func (m Model) runOrchestratorQueueRunFeatureSpecs() Model {
	agent, ok := m.selectedOrchestratorAgent()
	if !ok {
		m.orchestratorStatus = "No agent selected."
		return m
	}

	var req orchestrator.MainEnvRequest
	err := m.withOrchestratorStore(func(store *orchestrator.Store) error {
		var err error
		req, err = store.RequestMainEnv(agent.Name, "run_feature_specs", "", "")
		return err
	})
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Queue run_feature_specs failed for %q: %v", agent.Name, err)
		return m
	}
	if err := m.refreshOrchestratorData(); err != nil {
		m.orchestratorStatus = fmt.Sprintf("Queued request #%d; refresh failed: %v", req.ID, err)
		return m
	}
	m.orchestratorStatus = fmt.Sprintf("Queued run_feature_specs for %q as request #%d.", req.AgentName, req.ID)
	return m
}

func (m Model) runOrchestratorQueueDevServer(action string) Model {
	agent, ok := m.selectedOrchestratorAgent()
	if !ok {
		m.orchestratorStatus = "No agent selected."
		return m
	}

	payload := fmt.Sprintf(`{"action":"%s"}`, action)
	var req orchestrator.MainEnvRequest
	err := m.withOrchestratorStore(func(store *orchestrator.Store) error {
		var err error
		req, err = store.RequestMainEnv(agent.Name, "dev_server", payload, "")
		return err
	})
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Queue dev_server.%s failed for %q: %v", action, agent.Name, err)
		return m
	}
	if err := m.refreshOrchestratorData(); err != nil {
		m.orchestratorStatus = fmt.Sprintf("Queued request #%d; refresh failed: %v", req.ID, err)
		return m
	}
	m.orchestratorStatus = fmt.Sprintf("Queued dev_server.%s for %q as request #%d.", action, req.AgentName, req.ID)
	return m
}

func (m Model) runOrchestratorGrantNext() Model {
	var granted *orchestrator.MainEnvRequest
	err := m.withOrchestratorStore(func(store *orchestrator.Store) error {
		var err error
		granted, err = store.GrantNextMainEnv("normal", 10*time.Minute)
		return err
	})
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Grant-next failed: %v", err)
		return m
	}
	if granted == nil {
		m.orchestratorStatus = "No queued main environment requests."
		return m
	}
	if err := m.refreshOrchestratorData(); err != nil {
		m.orchestratorStatus = fmt.Sprintf("Granted request #%d; refresh failed: %v", granted.ID, err)
		return m
	}
	m.orchestratorStatus = fmt.Sprintf("Granted request #%d to %q (mode=normal ttl=10m).", granted.ID, granted.AgentName)
	return m
}

func (m Model) runOrchestratorRunNext() Model {
	cfg, err := orchestrator.LoadGatewayConfig("")
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Gateway config error: %v", err)
		return m
	}

	ttl := 10 * time.Minute
	var granted *orchestrator.MainEnvRequest
	var result orchestrator.GatewayResult
	err = m.withOrchestratorStore(func(store *orchestrator.Store) error {
		var err error
		granted, err = store.GrantNextMainEnv("normal", ttl)
		if err != nil || granted == nil {
			return err
		}

		stopAutoRenew := startMainEnvAutoRenewLoop(store, ttl)
		result = orchestrator.ExecuteMainEnvRequest(*granted, cfg)
		autoRenewErr := stopAutoRenew()

		resultJSON, _ := json.Marshal(result)
		releaseErr := store.ReleaseMainEnv(string(resultJSON), !result.Success)
		if releaseErr != nil {
			return releaseErr
		}
		if autoRenewErr != nil {
			return autoRenewErr
		}

		return nil
	})
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Run-next failed: %v", err)
		return m
	}
	if granted == nil {
		m.orchestratorStatus = "No queued main environment requests."
		return m
	}
	if err := m.refreshOrchestratorData(); err != nil {
		m.orchestratorStatus = fmt.Sprintf("Executed request #%d; refresh failed: %v", granted.ID, err)
		return m
	}
	if result.Success {
		m.orchestratorStatus = fmt.Sprintf("Executed request #%d for %q successfully (exit=%d).", granted.ID, granted.AgentName, result.ExitCode)
		return m
	}
	if result.Error != "" {
		m.orchestratorStatus = fmt.Sprintf("Executed request #%d for %q but failed: %s", granted.ID, granted.AgentName, result.Error)
		return m
	}
	m.orchestratorStatus = fmt.Sprintf("Executed request #%d for %q but failed (exit=%d).", granted.ID, granted.AgentName, result.ExitCode)
	return m
}

func (m Model) runOrchestratorRenewLease() Model {
	var lease orchestrator.LeaseInfo
	err := m.withOrchestratorStore(func(store *orchestrator.Store) error {
		var err error
		lease, err = store.RenewMainEnvLease(10 * time.Minute)
		return err
	})
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Renew lease failed: %v", err)
		return m
	}
	if err := m.refreshOrchestratorData(); err != nil {
		m.orchestratorStatus = fmt.Sprintf("Renewed lease for %q; refresh failed: %v", lease.HolderAgentName, err)
		return m
	}
	m.orchestratorStatus = fmt.Sprintf("Renewed lease for %q to %s.", lease.HolderAgentName, lease.ExpiresAt)
	return m
}

func (m Model) runOrchestratorReleaseSuccess() Model {
	err := m.withOrchestratorStore(func(store *orchestrator.Store) error {
		return store.ReleaseMainEnv("", false)
	})
	if err != nil {
		m.orchestratorStatus = fmt.Sprintf("Release lease failed: %v", err)
		return m
	}
	if err := m.refreshOrchestratorData(); err != nil {
		m.orchestratorStatus = fmt.Sprintf("Released lease; refresh failed: %v", err)
		return m
	}
	m.orchestratorStatus = "Released main environment lease as success."
	return m
}

func (m Model) renderOrchestrator() string {
	var b strings.Builder
	b.WriteString(titleStyle.Width(m.width).Render(" claude-manager — Orchestrator"))
	b.WriteString("\n")

	holder := "none"
	if m.orchestratorMainEnv.Lease.HolderAgentName != "" {
		holder = m.orchestratorMainEnv.Lease.HolderAgentName
	}
	expires := "-"
	if m.orchestratorMainEnv.Lease.ExpiresAt != "" {
		expires = m.orchestratorMainEnv.Lease.ExpiresAt
	}
	mode := m.orchestratorMainEnv.Lease.Mode
	if mode == "" {
		mode = "normal"
	}
	active := "no"
	if m.orchestratorMainEnv.Lease.Active {
		active = "yes"
	}

	leaseSummary := fmt.Sprintf("Lease active: %s  Holder: %s  Mode: %s  Expires: %s  Queue depth: %d",
		active,
		holder,
		mode,
		expires,
		m.orchestratorMainEnv.QueueDepth,
	)
	b.WriteString(lipgloss.NewStyle().Padding(0, 1).Foreground(white).Render(truncate(leaseSummary, max(20, m.width-2))))
	b.WriteString("\n")

	layoutHeight := m.height - 7
	if layoutHeight < 8 {
		layoutHeight = 8
	}
	agentsHeight := layoutHeight / 2
	if agentsHeight < 4 {
		agentsHeight = 4
	}
	queueHeight := layoutHeight - agentsHeight
	if queueHeight < 3 {
		queueHeight = 3
	}

	b.WriteString(m.renderOrchestratorAgents(agentsHeight))
	b.WriteString("\n")
	b.WriteString(m.renderOrchestratorQueue(queueHeight))
	b.WriteString("\n")
	if m.orchestratorAdding {
		b.WriteString(m.renderOrchestratorAddForm())
		b.WriteString("\n")
	}

	status := m.orchestratorStatus
	if strings.TrimSpace(status) == "" {
		status = "Ready. Press r to refresh."
	}
	b.WriteString(statusBarStyle.Width(m.width).Render(" " + truncate(status, max(20, m.width-2))))
	b.WriteString("\n")
	help := "Esc back • q quit • r refresh • a add-agent • ↑↓/j/k select • Enter/v attach • s start • S start-all • x stop • X force-stop • h heartbeat • f queue specs • d queue dev status • D queue dev start • n grant-next • R run-next • u renew • l release"
	b.WriteString(helpStyle.Render(truncate(help, max(20, m.width-2))))
	return b.String()
}

func (m Model) renderOrchestratorAgents(height int) string {
	var b strings.Builder
	title := lipgloss.NewStyle().Foreground(highlight).Bold(true).Render("Agents")
	b.WriteString(lipgloss.NewStyle().Padding(0, 1).Render(title))
	b.WriteString("\n")

	nameW := 16
	statusW := 16
	sessionW := 14
	workspaceW := m.width - 6 - nameW - statusW - sessionW
	if workspaceW < 12 {
		workspaceW = 12
	}

	header := fmt.Sprintf("%-*s %-*s %-*s %-*s", nameW, "name", statusW, "status", sessionW, "session_id", workspaceW, "workspace")
	b.WriteString(lipgloss.NewStyle().Foreground(dimText).PaddingLeft(1).Render(header))
	b.WriteString("\n")

	rows := height - 2
	if rows < 1 {
		rows = 1
	}
	if len(m.orchestratorAgents) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).PaddingLeft(1).Render("No agents found. Press a to add one (or use CLI: claude-manager agent add <name> <workspace>)."))
		return b.String()
	}

	start := 0
	if m.orchestratorCursor >= rows {
		start = m.orchestratorCursor - rows + 1
	}
	end := start + rows
	if end > len(m.orchestratorAgents) {
		end = len(m.orchestratorAgents)
	}

	for i := start; i < end; i++ {
		a := m.orchestratorAgents[i]
		line := fmt.Sprintf("%-*s %-*s %-*s %-*s",
			nameW, truncate(a.Name, nameW),
			statusW, truncate(a.Status, statusW),
			sessionW, truncate(a.SessionID, sessionW),
			workspaceW, truncate(a.WorkspacePath, workspaceW),
		)
		if i == m.orchestratorCursor {
			b.WriteString(selectedItemStyle.Render(line))
		} else {
			b.WriteString(itemStyle.Render(line))
		}
		b.WriteString("\n")
	}

	rendered := end - start
	for i := rendered; i < rows; i++ {
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderOrchestratorQueue(height int) string {
	var b strings.Builder
	title := lipgloss.NewStyle().Foreground(highlight).Bold(true).Render("Main Env Queue")
	b.WriteString(lipgloss.NewStyle().Padding(0, 1).Render(title))
	b.WriteString("\n")

	idW := 6
	agentW := 14
	typeW := 18
	statusW := 10
	queuedW := m.width - 6 - idW - agentW - typeW - statusW
	if queuedW < 16 {
		queuedW = 16
	}

	header := fmt.Sprintf("%-*s %-*s %-*s %-*s %-*s", idW, "id", agentW, "agent", typeW, "type", statusW, "status", queuedW, "queued_at")
	b.WriteString(lipgloss.NewStyle().Foreground(dimText).PaddingLeft(1).Render(header))
	b.WriteString("\n")

	rows := height - 2
	if rows < 1 {
		rows = 1
	}
	if len(m.orchestratorQueue) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).PaddingLeft(1).Render("Queue is empty."))
		return b.String()
	}

	for i := 0; i < len(m.orchestratorQueue) && i < rows; i++ {
		req := m.orchestratorQueue[i]
		line := fmt.Sprintf("%-*d %-*s %-*s %-*s %-*s",
			idW, req.ID,
			agentW, truncate(req.AgentName, agentW),
			typeW, truncate(req.RequestType, typeW),
			statusW, truncate(req.Status, statusW),
			queuedW, truncate(req.QueuedAt, queuedW),
		)
		b.WriteString(itemStyle.Render(line))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderOrchestratorAddForm() string {
	if len(m.orchestratorAddInputs) != orchestratorAddFieldCount {
		return ""
	}

	width := m.width - 8
	if width > 96 {
		width = 96
	}
	if width < 40 {
		width = 40
	}

	inputWidth := width - 22
	if inputWidth < 16 {
		inputWidth = 16
	}

	nameInput := m.orchestratorAddInputs[orchestratorAddFieldName]
	workspaceInput := m.orchestratorAddInputs[orchestratorAddFieldWorkspace]
	nameInput.Width = inputWidth
	workspaceInput.Width = inputWidth

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(highlight).Render("Add Agent"))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("%-16s %s", "Agent Name", nameInput.View()))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("%-16s %s", "Workspace Path", workspaceInput.View()))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(dimText).Render("Tab/Shift+Tab switch fields • Enter submit • Esc cancel"))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(highlight).
		Padding(0, 1).
		MarginLeft(1).
		Width(width).
		Render(b.String())
}

func (m Model) renderWorktrees() string {
	var b strings.Builder
	b.WriteString(titleStyle.Width(m.width).Render(" claude-manager — Worktree Mode"))
	b.WriteString("\n\n")

	lines := []string{
		"Worktree mode now uses Claude Code built-in support.",
		"",
		"claude-manager always starts Claude with --worktree.",
		"Claude creates/manages the worktree automatically for new or resumed sessions.",
		"",
		"claude-manager no longer creates git worktrees or session symlinks itself.",
		"The main environment should be controlled via orchestrator actions.",
	}
	for _, line := range lines {
		b.WriteString(lipgloss.NewStyle().Foreground(white).Padding(0, 2).Render(line))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("Esc back • q quit"))
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
		{"o", "Open orchestrator mode"},
		{"w/t", "Show worktree mode info"},
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

func startMainEnvAutoRenewLoop(store *orchestrator.Store, ttl time.Duration) func() error {
	interval := ttl / 2
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	if interval < 1*time.Second {
		interval = 1 * time.Second
	}

	stopCh := make(chan struct{})
	doneCh := make(chan error, 1)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if _, err := store.RenewMainEnvLease(ttl); err != nil {
					if strings.Contains(err.Error(), "no active holder") {
						continue
					}
					doneCh <- err
					return
				}
			case <-stopCh:
				doneCh <- nil
				return
			}
		}
	}()

	return func() error {
		close(stopCh)
		return <-doneCh
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
