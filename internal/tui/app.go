package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"claude-manager/internal/ai"
	"claude-manager/internal/sessions"
	"claude-manager/internal/worktree"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type searchMode int

const (
	searchQuick    searchMode = iota // summary/project/branch match
	searchFullText                   // all message text
	searchSemantic                   // embedding cosine similarity
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
	newSessionBranch string // chosen branch (empty = current branch, no switch)
	showNewSession  bool
	newSessionPaths []projectEntry
	newSessionCursor int
	showNewSessionBranch bool
	newSessionBranches   []string // "(current branch)" sentinel + real branches
	newSessionBranchFiltered []string // filtered view used for display
	newSessionBranchCursor int
	newSessionBranchSearch textinput.Model

	// Name-input step (after base branch is picked)
	showNewSessionBranchName bool
	newSessionBranchName     textinput.Model
	newSessionBaseBranch     string
	cwd             string // working directory where claude-manager was launched
	cwdFilter       bool   // true = only show sessions whose ProjectPath is under cwd
	scopeMsg        string // one-shot note about scope (e.g. fallback to all)
	fullTextSearch  bool // true = search all message text, false = summary/project/branch only
	SkipPermissions bool // pass --dangerously-skip-permissions to claude
	UseWorktree     bool // resume in a new git worktree
	showWorktrees        bool
	worktrees            []worktree.Entry
	worktreeCursor       int
	worktreeMsg          string // feedback after removal
	worktreeForceConfirm bool   // true when normal remove failed, awaiting f to force
	worktreeCreating     bool   // true when showing branch picker for new worktree
	worktreeBranches     []string
	worktreeBranchCursor int
	worktreeRepoRoot     string // repo to create worktree in

	// Summary generation
	summaryCache   *ai.SummaryCache
	summaryLoading map[string]bool

	// Embedding search
	embeddingStore  *ai.EmbeddingStore
	openaiConfig    *ai.OpenAIConfig
	searchModeVal   searchMode
	semanticResults []ai.SearchResult
	semanticLoading bool
	embeddingStatus string

	// Settings screen
	showSettings     bool
	settingsCursor   int
	settingsEditing  bool
	settingsInput    textinput.Model
	settingsFields   []settingsField
	settingsMsg      string
}

type projectEntry struct {
	Name string
	Path string
}

type settingsField struct {
	Label    string
	Key      string // field name in ai.Settings
	Value    string
	Secret   bool // mask display
}

// NewModel creates a new TUI model with the given sessions.
func NewModel(ss []sessions.Session, cwd string) Model {
	ti := textinput.New()
	ti.Placeholder = "Search... (@repo to filter by project)"
	ti.CharLimit = 100

	branchSearch := textinput.New()
	branchSearch.Placeholder = "filter branches..."
	branchSearch.CharLimit = 100
	branchSearch.Prompt = "/ "

	branchName := textinput.New()
	branchName.Placeholder = "new-branch-name (empty = use base branch as-is)"
	branchName.CharLimit = 100
	branchName.Prompt = "> "

	summaryCache := ai.NewSummaryCache()
	openaiConfig := ai.ResolveOpenAI()

	// Pre-populate LLM summaries from cache (no API calls)
	for i := range ss {
		if cached := summaryCache.Get(ss[i].ID); cached != "" {
			ss[i].LLMSummary = cached
		}
	}

	var embeddingStore *ai.EmbeddingStore
	if openaiConfig != nil {
		store, err := ai.NewEmbeddingStore()
		if err == nil {
			embeddingStore = store
		}
	}

	m := Model{
		allSessions:      ss,
		filteredSessions: ss,
		search:           ti,
		cwd:              cwd,
		summaryCache:     summaryCache,
		summaryLoading:   make(map[string]bool),
		embeddingStore:   embeddingStore,
		openaiConfig:     openaiConfig,
		newSessionBranchSearch: branchSearch,
		newSessionBranchName:   branchName,
	}

	// Default to cwd-scoped view when running inside a directory that has sessions.
	if cwd != "" {
		scoped := filterByCwd(ss, cwd)
		if len(scoped) > 0 {
			m.cwdFilter = true
			m.filteredSessions = scoped
		} else {
			m.scopeMsg = fmt.Sprintf("no sessions for %s — showing all", cwd)
		}
	}

	return m
}

// Message types for worktree screen
type worktreesLoadedMsg struct {
	entries []worktree.Entry
}

type worktreeRemovedMsg struct {
	idx int
	err error
}

// Summary generation messages and commands

type summaryGeneratedMsg struct {
	sessionID string
	summary   string
	err       error
}

type summaryBatchDoneMsg struct{}

func generateSummaryCmd(cache *ai.SummaryCache, sess sessions.Session) tea.Cmd {
	return func() tea.Msg {
		context := sessions.BuildSummaryContext(sess.FilePath)
		if context == "" {
			return summaryGeneratedMsg{sessionID: sess.ID, err: fmt.Errorf("no context")}
		}
		summary, err := cache.Generate(sess.ID, context)
		return summaryGeneratedMsg{sessionID: sess.ID, summary: summary, err: err}
	}
}

// generateVisibleSummariesCmd fires summary generation for a batch of sessions
// that don't yet have summaries. Generates sequentially to avoid API spam.
func generateVisibleSummariesCmd(cache *ai.SummaryCache, ss []sessions.Session) tea.Cmd {
	return func() tea.Msg {
		for _, s := range ss {
			if s.LLMSummary != "" {
				continue
			}
			context := sessions.BuildSummaryContext(s.FilePath)
			if context == "" {
				continue
			}
			cache.Generate(s.ID, context)
		}
		return summaryBatchDoneMsg{}
	}
}

// maybeTriggerSummary fires a summary generation command if the currently
// selected session has no LLM summary and an API key is available.
func (m *Model) maybeTriggerSummary() tea.Cmd {
	if !ai.SummaryAvailable() {
		return nil
	}
	if m.cursor >= len(m.filteredSessions) {
		return nil
	}
	sess := m.filteredSessions[m.cursor]
	if sess.LLMSummary != "" || m.summaryLoading[sess.ID] {
		return nil
	}
	m.summaryLoading[sess.ID] = true
	return generateSummaryCmd(m.summaryCache, sess)
}

// Embedding messages and commands

type embeddingsIndexedMsg struct {
	count int
	err   error
}

type semanticSearchResultMsg struct {
	results []ai.SearchResult
	err     error
}

func indexEmbeddingsCmd(store *ai.EmbeddingStore, config *ai.OpenAIConfig, ss []sessions.Session) tea.Cmd {
	return func() tea.Msg {
		count, err := store.EnsureEmbeddings(config, ss)
		return embeddingsIndexedMsg{count: count, err: err}
	}
}

func semanticSearchCmd(store *ai.EmbeddingStore, config *ai.OpenAIConfig, query string, ss []sessions.Session) tea.Cmd {
	return func() tea.Msg {
		results, err := store.SemanticSearch(config, query, ss, 20)
		return semanticSearchResultMsg{results: results, err: err}
	}
}

type worktreeCreatedMsg struct {
	result worktree.CreateResult
	err    error
}

type branchesLoadedMsg struct {
	branches []string
	repoRoot string
}

func discoverWorktreesCmd(ss []sessions.Session) tea.Cmd {
	return func() tea.Msg {
		return worktreesLoadedMsg{entries: worktree.Discover(ss)}
	}
}

func removeWorktreeCmd(entries []worktree.Entry, idx int, force bool) tea.Cmd {
	return func() tea.Msg {
		err := worktree.Remove(entries[idx], force)
		return worktreeRemovedMsg{idx: idx, err: err}
	}
}

func listBranchesCmd(repoRoot string) tea.Cmd {
	return func() tea.Msg {
		return branchesLoadedMsg{branches: worktree.ListBranches(repoRoot), repoRoot: repoRoot}
	}
}

type newSessionBranchesLoadedMsg struct {
	branches []string
}

func listNewSessionBranchesCmd(projectPath string) tea.Cmd {
	return func() tea.Msg {
		return newSessionBranchesLoadedMsg{branches: worktree.ListBranches(projectPath)}
	}
}

func createWorktreeCmd(repoRoot, branch string) tea.Cmd {
	return func() tea.Msg {
		// TUI can't prompt on stdin — auto-derive on collision and let the user
		// see the chosen branch in worktreeMsg.
		res, err := worktree.CreateWithStrategy(repoRoot, branch, "", worktree.CreateStrategy{OnCollision: worktree.CollisionDerive})
		return worktreeCreatedMsg{result: res, err: err}
	}
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.SetWindowTitle("claude-manager")}
	// Generate summaries for first batch of sessions
	if ai.SummaryAvailable() && len(m.allSessions) > 0 {
		batch := m.allSessions
		if len(batch) > 30 {
			batch = batch[:30]
		}
		cmds = append(cmds, generateVisibleSummariesCmd(m.summaryCache, batch))
	}
	if m.embeddingStore != nil && m.openaiConfig != nil {
		cmds = append(cmds, indexEmbeddingsCmd(m.embeddingStore, m.openaiConfig, m.allSessions))
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case embeddingsIndexedMsg:
		if msg.err == nil && msg.count > 0 {
			m.embeddingStatus = fmt.Sprintf("indexed %d sessions", msg.count)
		}
		return m, nil

	case semanticSearchResultMsg:
		m.semanticLoading = false
		if msg.err == nil {
			m.semanticResults = msg.results
			m.filteredSessions = m.filteredSessions[:0]
			for _, r := range msg.results {
				if m.cwdFilter && !sessionInCwd(r.Session, m.cwd) {
					continue
				}
				m.filteredSessions = append(m.filteredSessions, r.Session)
			}
			m.cursor = 0
		}
		return m, m.maybeTriggerSummary()

	case worktreesLoadedMsg:
		m.worktrees = msg.entries
		m.worktreeCursor = 0
		m.worktreeMsg = ""
		m.worktreeForceConfirm = false
		m.worktreeCreating = false
		return m, nil

	case branchesLoadedMsg:
		m.worktreeBranches = msg.branches
		m.worktreeBranchCursor = 0
		m.worktreeRepoRoot = msg.repoRoot
		m.worktreeCreating = true
		return m, nil

	case newSessionBranchesLoadedMsg:
		// Prepend sentinel for "use current branch"
		m.newSessionBranches = append([]string{"(current branch)"}, msg.branches...)
		m.newSessionBranchFiltered = m.newSessionBranches
		m.newSessionBranchCursor = 0
		m.newSessionBranchSearch.SetValue("")
		m.showNewSessionBranch = true
		return m, nil

	case worktreeCreatedMsg:
		if msg.err != nil {
			m.worktreeMsg = fmt.Sprintf("Error: %v", msg.err)
			m.worktreeCreating = false
		} else {
			if msg.result.DerivedFrom != "" {
				m.worktreeMsg = fmt.Sprintf("Created %s — branch %s was already in use, created new branch %s off it",
					msg.result.Path, msg.result.DerivedFrom, msg.result.Branch)
			} else {
				m.worktreeMsg = fmt.Sprintf("Created %s", msg.result.Path)
			}
			m.worktreeCreating = false
			return m, discoverWorktreesCmd(m.allSessions)
		}
		return m, nil

	case summaryBatchDoneMsg:
		// Reload all cached summaries into session lists
		for i := range m.allSessions {
			if cached := m.summaryCache.Get(m.allSessions[i].ID); cached != "" {
				m.allSessions[i].LLMSummary = cached
			}
		}
		for i := range m.filteredSessions {
			if cached := m.summaryCache.Get(m.filteredSessions[i].ID); cached != "" {
				m.filteredSessions[i].LLMSummary = cached
			}
		}
		return m, nil

	case summaryGeneratedMsg:
		delete(m.summaryLoading, msg.sessionID)
		if msg.err == nil && msg.summary != "" {
			for i := range m.allSessions {
				if m.allSessions[i].ID == msg.sessionID {
					m.allSessions[i].LLMSummary = msg.summary
				}
			}
			for i := range m.filteredSessions {
				if m.filteredSessions[i].ID == msg.sessionID {
					m.filteredSessions[i].LLMSummary = msg.summary
				}
			}
		}
		return m, nil

	case worktreeRemovedMsg:
		if msg.err != nil {
			m.worktreeMsg = fmt.Sprintf("Error: %v — press f to force", msg.err)
			m.worktreeForceConfirm = true
		} else {
			m.worktreeMsg = fmt.Sprintf("Removed %s", m.worktrees[msg.idx].Path)
			m.worktreeForceConfirm = false
			m.worktrees = append(m.worktrees[:msg.idx], m.worktrees[msg.idx+1:]...)
			if m.worktreeCursor >= len(m.worktrees) && m.worktreeCursor > 0 {
				m.worktreeCursor--
			}
		}
		return m, nil

	case tea.KeyMsg:
		if m.showSettings {
			return m.handleSettingsKey(msg)
		}
		if m.showNewSessionBranchName {
			return m.handleNewSessionBranchNameKey(msg)
		}
		if m.showNewSessionBranch {
			return m.handleNewSessionBranchKey(msg)
		}
		if m.showNewSession {
			return m.handleNewSessionKey(msg)
		}
		if m.showWorktrees && m.worktreeCreating {
			return m.handleWorktreeBranchKey(msg)
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
		m.semanticResults = nil
		m.searchModeVal = searchQuick
		m.fullTextSearch = false
		m.applyFilters()
		return m, nil

	case "enter", "tab":
		m.searching = false
		m.search.Blur()
		if m.searchModeVal == searchSemantic && m.search.Value() != "" && m.embeddingStore != nil {
			m.semanticLoading = true
			return m, semanticSearchCmd(m.embeddingStore, m.openaiConfig, m.search.Value(), m.allSessions)
		}
		return m, nil

	case "ctrl+f":
		switch m.searchModeVal {
		case searchQuick:
			m.searchModeVal = searchFullText
			m.fullTextSearch = true
		case searchFullText:
			if m.embeddingStore != nil && m.openaiConfig != nil {
				m.searchModeVal = searchSemantic
				m.fullTextSearch = false
			} else {
				m.searchModeVal = searchQuick
				m.fullTextSearch = false
			}
		case searchSemantic:
			m.searchModeVal = searchQuick
			m.fullTextSearch = false
			m.semanticResults = nil
		}
		m.applyFilters()
		return m, nil

	default:
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		if m.searchModeVal == searchSemantic {
			// Don't apply local filters in semantic mode; wait for enter/tab
		} else {
			m.applyFilters()
		}
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
			m.semanticResults = nil
			m.searchModeVal = searchQuick
			m.fullTextSearch = false
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
		return m, m.maybeTriggerSummary()

	case "down", "j":
		if m.cursor < len(m.filteredSessions)-1 {
			m.cursor++
		}
		return m, m.maybeTriggerSummary()

	case "home", "g":
		m.cursor = 0
		return m, m.maybeTriggerSummary()

	case "end", "G":
		if len(m.filteredSessions) > 0 {
			m.cursor = len(m.filteredSessions) - 1
		}
		return m, m.maybeTriggerSummary()

	case "pgup":
		m.cursor -= 10
		if m.cursor < 0 {
			m.cursor = 0
		}
		return m, m.maybeTriggerSummary()

	case "pgdown":
		m.cursor += 10
		if max := len(m.filteredSessions) - 1; m.cursor > max {
			m.cursor = max
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		return m, m.maybeTriggerSummary()

	case "!":
		m.SkipPermissions = !m.SkipPermissions
		return m, nil

	case "w":
		m.UseWorktree = !m.UseWorktree
		return m, nil

	case "a":
		m.cwdFilter = !m.cwdFilter
		m.scopeMsg = ""
		m.applyFilters()
		return m, m.maybeTriggerSummary()

	case "s":
		m.showSettings = true
		m.settingsCursor = 0
		m.settingsEditing = false
		m.settingsMsg = ""
		m.loadSettingsFields()
		return m, nil

	case "t":
		m.showWorktrees = true
		m.worktreeMsg = ""
		m.worktreeForceConfirm = false
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
			m.worktreeForceConfirm = false
		}
		return m, nil

	case "down", "j":
		if m.worktreeCursor < len(m.worktrees)-1 {
			m.worktreeCursor++
			m.worktreeForceConfirm = false
		}
		return m, nil

	case "d", "x":
		if len(m.worktrees) > 0 && m.worktreeCursor < len(m.worktrees) {
			m.worktreeMsg = fmt.Sprintf("Removing %s...", m.worktrees[m.worktreeCursor].Path)
			m.worktreeForceConfirm = false
			return m, removeWorktreeCmd(m.worktrees, m.worktreeCursor, false)
		}
		return m, nil

	case "f":
		if m.worktreeForceConfirm && len(m.worktrees) > 0 && m.worktreeCursor < len(m.worktrees) {
			m.worktreeMsg = fmt.Sprintf("Force removing %s...", m.worktrees[m.worktreeCursor].Path)
			m.worktreeForceConfirm = false
			return m, removeWorktreeCmd(m.worktrees, m.worktreeCursor, true)
		}
		return m, nil

	case "n":
		// Create new worktree — use repo root of selected worktree, or cwd
		repoRoot := m.cwd
		if len(m.worktrees) > 0 && m.worktreeCursor < len(m.worktrees) {
			repoRoot = m.worktrees[m.worktreeCursor].RepoRoot
		}
		if repoRoot != "" {
			return m, listBranchesCmd(repoRoot)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleWorktreeBranchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.worktreeCreating = false
		return m, nil

	case "q", "ctrl+c":
		return m, tea.Quit

	case "up", "k":
		if m.worktreeBranchCursor > 0 {
			m.worktreeBranchCursor--
		}
		return m, nil

	case "down", "j":
		if m.worktreeBranchCursor < len(m.worktreeBranches)-1 {
			m.worktreeBranchCursor++
		}
		return m, nil

	case "enter":
		if len(m.worktreeBranches) > 0 && m.worktreeBranchCursor < len(m.worktreeBranches) {
			branch := m.worktreeBranches[m.worktreeBranchCursor]
			m.worktreeMsg = fmt.Sprintf("Creating worktree for %s...", branch)
			return m, createWorktreeCmd(m.worktreeRepoRoot, branch)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) buildProjectList() []projectEntry {
	seenPath := map[string]bool{}
	seenProject := map[string]bool{}
	var entries []projectEntry

	// Current directory first
	if m.cwd != "" {
		entries = append(entries, projectEntry{
			Name: filepath.Base(m.cwd) + " (current dir)",
			Path: m.cwd,
		})
		seenPath[m.cwd] = true
		seenProject[filepath.Base(m.cwd)] = true
	}

	// First pass: non-worktree paths (canonical project roots)
	for _, s := range m.allSessions {
		if s.ProjectPath == "" || seenPath[s.ProjectPath] || s.IsWorktree() {
			continue
		}
		if seenProject[s.Project] {
			continue
		}
		seenPath[s.ProjectPath] = true
		seenProject[s.Project] = true
		entries = append(entries, projectEntry{
			Name: s.Project,
			Path: s.ProjectPath,
		})
	}

	// Second pass: worktree paths for projects not yet represented
	for _, s := range m.allSessions {
		if s.ProjectPath == "" || seenPath[s.ProjectPath] {
			continue
		}
		if seenProject[s.Project] {
			continue
		}
		seenPath[s.ProjectPath] = true
		seenProject[s.Project] = true
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
			m.newSessionPath = m.newSessionPaths[m.newSessionCursor].Path
			return m, listNewSessionBranchesCmd(m.newSessionPath)
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) applyBranchFilter() {
	q := strings.ToLower(strings.TrimSpace(m.newSessionBranchSearch.Value()))
	if q == "" {
		m.newSessionBranchFiltered = m.newSessionBranches
	} else {
		filtered := make([]string, 0, len(m.newSessionBranches))
		// Always keep sentinel "(current branch)" as first option
		filtered = append(filtered, m.newSessionBranches[0])
		for _, b := range m.newSessionBranches[1:] {
			if strings.Contains(strings.ToLower(b), q) {
				filtered = append(filtered, b)
			}
		}
		m.newSessionBranchFiltered = filtered
	}
	if m.newSessionBranchCursor >= len(m.newSessionBranchFiltered) {
		m.newSessionBranchCursor = len(m.newSessionBranchFiltered) - 1
	}
	if m.newSessionBranchCursor < 0 {
		m.newSessionBranchCursor = 0
	}
}

func (m Model) handleNewSessionBranchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.newSessionBranchSearch.Focused() {
			m.newSessionBranchSearch.Blur()
			m.newSessionBranchSearch.SetValue("")
			m.applyBranchFilter()
			return m, nil
		}
		m.showNewSessionBranch = false
		return m, nil

	case "ctrl+c":
		return m, tea.Quit

	case "up":
		if m.newSessionBranchCursor > 0 {
			m.newSessionBranchCursor--
		}
		return m, nil

	case "down":
		if m.newSessionBranchCursor < len(m.newSessionBranchFiltered)-1 {
			m.newSessionBranchCursor++
		}
		return m, nil

	case "enter":
		if m.newSessionBranchSearch.Focused() {
			m.newSessionBranchSearch.Blur()
			return m, nil
		}
		if len(m.newSessionBranchFiltered) > 0 && m.newSessionBranchCursor < len(m.newSessionBranchFiltered) {
			m.newSession = true
			picked := m.newSessionBranchFiltered[m.newSessionBranchCursor]
			if picked != m.newSessionBranches[0] {
				m.newSessionBranch = picked
			}
			return m, tea.Quit
		}
		return m, nil
	}

	// If focused, all other keys feed the search input
	if m.newSessionBranchSearch.Focused() {
		var cmd tea.Cmd
		m.newSessionBranchSearch, cmd = m.newSessionBranchSearch.Update(msg)
		m.applyBranchFilter()
		return m, cmd
	}

	// Unfocused shortcuts
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "k":
		if m.newSessionBranchCursor > 0 {
			m.newSessionBranchCursor--
		}
		return m, nil
	case "j":
		if m.newSessionBranchCursor < len(m.newSessionBranchFiltered)-1 {
			m.newSessionBranchCursor++
		}
		return m, nil
	case "!":
		m.SkipPermissions = !m.SkipPermissions
		return m, nil
	case "w":
		m.UseWorktree = !m.UseWorktree
		return m, nil
	case "/":
		m.newSessionBranchSearch.Focus()
		return m, nil
	case "n":
		// Treat selected branch as the BASE for a new branch; advance to name input.
		if len(m.newSessionBranchFiltered) > 0 && m.newSessionBranchCursor < len(m.newSessionBranchFiltered) {
			picked := m.newSessionBranchFiltered[m.newSessionBranchCursor]
			if picked == m.newSessionBranches[0] {
				m.newSessionBaseBranch = "" // current branch
			} else {
				m.newSessionBaseBranch = picked
			}
			m.showNewSessionBranchName = true
			m.newSessionBranchName.SetValue("")
			m.newSessionBranchName.Focus()
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleNewSessionBranchNameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.showNewSessionBranchName = false
		m.newSessionBranchName.Blur()
		return m, nil

	case "ctrl+c":
		return m, tea.Quit

	case "enter":
		name := strings.TrimSpace(m.newSessionBranchName.Value())
		if name == "" {
			return m, nil
		}
		m.newSession = true
		m.newSessionBranch = name
		return m, tea.Quit
	}

	var cmd tea.Cmd
	m.newSessionBranchName, cmd = m.newSessionBranchName.Update(msg)
	return m, cmd
}

func (m Model) renderNewSessionBranchName() string {
	var b strings.Builder
	b.WriteString(titleStyle.Width(m.width).Render(" claude-manager — New Branch Name"))
	b.WriteString("\n\n")
	base := m.newSessionBaseBranch
	if base == "" {
		base = "(current branch)"
	}
	b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(0, 2).Render("Project: " + m.newSessionPath))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(0, 2).Render("Base:    " + base))
	b.WriteString("\n\n")
	b.WriteString(lipgloss.NewStyle().Padding(0, 2).Render(m.newSessionBranchName.View()))
	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render("enter confirm • Esc back • Ctrl+C quit"))
	return b.String()
}

func (m Model) renderNewSessionBranch() string {
	var b strings.Builder
	b.WriteString(titleStyle.Width(m.width).Render(" claude-manager — Select Branch"))
	b.WriteString("\n\n")
	b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(0, 2).Render("Project: " + m.newSessionPath))
	b.WriteString("\n")

	// Search input
	b.WriteString(lipgloss.NewStyle().Padding(0, 2).Render(m.newSessionBranchSearch.View()))
	b.WriteString("\n\n")

	if len(m.newSessionBranchFiltered) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(1, 2).Render("No branches match"))
		b.WriteString("\n")
	} else {
		listHeight := m.height - 11
		if listHeight < 3 {
			listHeight = 3
		}
		start := 0
		if m.newSessionBranchCursor >= listHeight {
			start = m.newSessionBranchCursor - listHeight + 1
		}
		end := start + listHeight
		if end > len(m.newSessionBranchFiltered) {
			end = len(m.newSessionBranchFiltered)
		}

		sentinel := m.newSessionBranches[0]
		for i := start; i < end; i++ {
			branch := m.newSessionBranchFiltered[i]
			style := lipgloss.NewStyle().Foreground(special)
			if branch == sentinel {
				style = lipgloss.NewStyle().Foreground(dimText).Italic(true)
			}
			line := style.Render(branch)
			if i == m.newSessionBranchCursor {
				b.WriteString(selectedItemStyle.Render(line))
			} else {
				b.WriteString(itemStyle.Render(line))
			}
			b.WriteString("\n")
		}
	}

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
	help := "↑↓ nav • enter use branch • n new branch from base • / search • ! skip-perms • w worktree • Esc back • q quit"
	if m.newSessionBranchSearch.Focused() {
		help = "type to filter • enter/esc exit search"
	}
	b.WriteString(helpStyle.Render(help))
	return b.String()
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

// applyFilters re-applies the cwd scope, @project filter, and search query.
func (m *Model) applyFilters() {
	project, query := parseQuery(m.search.Value())
	src := m.allSessions
	if m.cwdFilter {
		src = filterByCwd(src, m.cwd)
	}
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

// NewSessionBranch returns the branch chosen for the new session, or "" to use current branch.
func (m Model) NewSessionBranch() string {
	if !m.newSession {
		return ""
	}
	return m.newSessionBranch
}

// NewSessionBaseBranch returns the base branch when the user chose to create a new branch.
// Empty means either "use branch directly" or "base off current branch".
func (m Model) NewSessionBaseBranch() string {
	if !m.newSession {
		return ""
	}
	return m.newSessionBaseBranch
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	if m.showSettings {
		return m.renderSettings()
	}

	if m.showNewSessionBranchName {
		return m.renderNewSessionBranchName()
	}

	if m.showNewSessionBranch {
		return m.renderNewSessionBranch()
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
		switch m.searchModeVal {
		case searchSemantic:
			modeTag = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF79C6")).Render(" [semantic]")
		case searchFullText:
			modeTag = lipgloss.NewStyle().Foreground(special).Render(" [full-text]")
		default:
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
	detailHeight := m.height * 2 / 5
	if detailHeight < 10 {
		detailHeight = 10
	}
	if detailHeight > 18 {
		detailHeight = 18
	}

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

		// Build score map for semantic results
		scoreMap := make(map[string]float64)
		for _, r := range m.semanticResults {
			scoreMap[r.Session.ID] = r.Score
		}

		for i := start; i < end; i++ {
			selected := i == m.cursor
			score := scoreMap[m.filteredSessions[i].ID]
			b.WriteString(renderSessionItem(m.filteredSessions[i], m.width, selected, score))
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
	if m.cwdFilter {
		status += "  📁 scope: " + filepath.Base(m.cwd)
	} else {
		status += "  📁 scope: all"
	}
	if m.UseWorktree {
		status += "  🌳 worktree mode: ON"
	}
	if m.SkipPermissions {
		status += "  ⚡ skip-permissions"
	}
	if m.scopeMsg != "" {
		status += "  • " + m.scopeMsg
	}
	if m.embeddingStore != nil {
		count := m.embeddingStore.IndexedCount()
		if count > 0 {
			status += fmt.Sprintf("  🔮 %d indexed", count)
		}
	}
	b.WriteString(statusBarStyle.Width(m.width).Render(status))
	b.WriteString("\n")

	// Help bar
	var help string
	if m.searching {
		modeHelp := "quick/full-text"
		if m.embeddingStore != nil {
			modeHelp += "/semantic"
		}
		help = fmt.Sprintf("tab/enter confirm • ctrl+f cycle %s • esc cancel", modeHelp)
	} else {
		help = "↑↓ navigate • enter resume • n new • / search • a scope • s settings • t worktrees • w worktree • ! skip-perms • ? help • q quit"
	}
	b.WriteString(helpStyle.Render(help))

	return b.String()
}

func (m Model) renderWorktrees() string {
	var b strings.Builder

	if m.worktreeCreating {
		return m.renderWorktreeBranchPicker()
	}

	b.WriteString(titleStyle.Width(m.width).Render(" claude-manager — Worktrees"))
	b.WriteString("\n\n")

	if len(m.worktrees) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(1, 2).Render("No worktrees found"))
		b.WriteString("\n")
	} else {
		// Reserve space for detail panel
		detailHeight := 0
		hasSession := m.worktreeCursor < len(m.worktrees) && m.worktrees[m.worktreeCursor].Session != nil
		if hasSession {
			detailHeight = m.height * 2 / 5
			if detailHeight < 10 {
				detailHeight = 10
			}
			if detailHeight > 16 {
				detailHeight = 16
			}
		}

		listHeight := m.height - 6 - detailHeight // title + padding + help + msg
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

			branchCol := lipgloss.NewStyle().Foreground(special).Render(e.Branch)
			sessionCol := ""
			if e.SessionMsg != "" {
				sessionCol = "  " + lipgloss.NewStyle().Foreground(dimText).Render(e.SessionMsg)
			}

			line := fmt.Sprintf("%s  %s%s",
				lipgloss.NewStyle().Foreground(highlight).Bold(true).Width(18).Render(repo),
				branchCol,
				sessionCol,
			)
			if i == m.worktreeCursor {
				b.WriteString(selectedItemStyle.Render(line))
			} else {
				b.WriteString(itemStyle.Render(line))
			}
			b.WriteString("\n")
		}

		// Detail panel for selected worktree's session
		if hasSession && detailHeight > 3 {
			b.WriteString(renderDetail(*m.worktrees[m.worktreeCursor].Session, m.width, detailHeight))
			b.WriteString("\n")
		}
	}

	if m.worktreeMsg != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(0, 2).Render(m.worktreeMsg))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	help := "↑↓ navigate • d remove"
	if m.worktreeForceConfirm {
		help += " • f force remove"
	}
	help += " • n new worktree • Esc back • q quit"
	b.WriteString(helpStyle.Render(help))
	return b.String()
}

func (m Model) renderWorktreeBranchPicker() string {
	var b strings.Builder
	b.WriteString(titleStyle.Width(m.width).Render(" claude-manager — New Worktree"))
	b.WriteString("\n\n")

	if len(m.worktreeBranches) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(1, 2).Render("No branches found"))
		b.WriteString("\n")
	} else {
		b.WriteString(lipgloss.NewStyle().Foreground(dimText).Padding(0, 2).Render(
			fmt.Sprintf("Select branch for %s:", filepath.Base(m.worktreeRepoRoot)),
		))
		b.WriteString("\n\n")

		listHeight := m.height - 8
		if listHeight < 3 {
			listHeight = 3
		}
		start := 0
		if m.worktreeBranchCursor >= listHeight {
			start = m.worktreeBranchCursor - listHeight + 1
		}
		end := start + listHeight
		if end > len(m.worktreeBranches) {
			end = len(m.worktreeBranches)
		}

		for i := start; i < end; i++ {
			line := lipgloss.NewStyle().Foreground(special).Render(m.worktreeBranches[i])
			if i == m.worktreeBranchCursor {
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
	b.WriteString(helpStyle.Render("↑↓ navigate • enter create • Esc back • q quit"))
	return b.String()
}

// Settings screen

func (m *Model) loadSettingsFields() {
	s := ai.LoadSettings()
	m.settingsFields = []settingsField{
		{Label: "Summary Provider", Key: "summaryProvider", Value: string(s.SummaryProvider)},
		{Label: "Anthropic API Key", Key: "anthropicApiKey", Value: s.AnthropicAPIKey, Secret: true},
		{Label: "OpenAI API Key", Key: "openaiApiKey", Value: s.OpenAIAPIKey, Secret: true},
		{Label: "Compatible Base URL", Key: "compatibleBaseUrl", Value: s.CompatibleBaseURL},
		{Label: "Compatible API Key", Key: "compatibleApiKey", Value: s.CompatibleAPIKey, Secret: true},
		{Label: "Compatible Model", Key: "compatibleModel", Value: s.CompatibleModel},
		{Label: "Embedding Model", Key: "embeddingModel", Value: s.EmbeddingModel},
	}
}

func (m *Model) saveSettingsFields() {
	s := ai.Settings{
		SummaryProvider:   ai.SummaryProvider(m.settingsFields[0].Value),
		AnthropicAPIKey:   m.settingsFields[1].Value,
		OpenAIAPIKey:      m.settingsFields[2].Value,
		CompatibleBaseURL: m.settingsFields[3].Value,
		CompatibleAPIKey:  m.settingsFields[4].Value,
		CompatibleModel:   m.settingsFields[5].Value,
		EmbeddingModel:    m.settingsFields[6].Value,
	}
	if err := ai.SaveSettings(s); err != nil {
		m.settingsMsg = fmt.Sprintf("Error saving: %v", err)
	} else {
		m.settingsMsg = "Settings saved"
		// Re-initialize embedding store if OpenAI key changed
		if s.OpenAIAPIKey != "" || os.Getenv("OPENAI_API_KEY") != "" {
			m.openaiConfig = ai.ResolveOpenAI()
			if m.embeddingStore == nil {
				store, err := ai.NewEmbeddingStore()
				if err == nil {
					m.embeddingStore = store
				}
			}
		}
	}
}

func maskSecret(s string) string {
	if s == "" {
		return "(not set)"
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "..." + s[len(s)-4:]
}

func (m Model) handleSettingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.settingsEditing {
		switch msg.String() {
		case "esc":
			m.settingsEditing = false
			m.settingsInput.Blur()
			return m, nil
		case "enter":
			m.settingsFields[m.settingsCursor].Value = m.settingsInput.Value()
			m.settingsEditing = false
			m.settingsInput.Blur()
			m.saveSettingsFields()
			return m, nil
		default:
			var cmd tea.Cmd
			m.settingsInput, cmd = m.settingsInput.Update(msg)
			return m, cmd
		}
	}

	switch msg.String() {
	case "esc":
		m.showSettings = false
		return m, nil
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.settingsCursor > 0 {
			m.settingsCursor--
		}
		return m, nil
	case "down", "j":
		if m.settingsCursor < len(m.settingsFields)-1 {
			m.settingsCursor++
		}
		return m, nil
	case "enter":
		f := m.settingsFields[m.settingsCursor]
		m.settingsEditing = true
		m.settingsInput = textinput.New()
		if f.Key == "summaryProvider" {
			m.settingsInput.Placeholder = "anthropic, openai-compatible, or none"
		} else if f.Secret {
			m.settingsInput.Placeholder = "Enter API key..."
			m.settingsInput.EchoMode = textinput.EchoPassword
		} else {
			m.settingsInput.Placeholder = "Enter value..."
		}
		m.settingsInput.SetValue(f.Value)
		m.settingsInput.Focus()
		m.settingsInput.CharLimit = 200
		m.settingsInput.Width = 60
		m.settingsMsg = ""
		return m, textinput.Blink
	case "backspace", "delete":
		// Clear the field
		m.settingsFields[m.settingsCursor].Value = ""
		m.saveSettingsFields()
		return m, nil
	case "c":
		// Clear all caches
		m.summaryCache.ClearAll()
		if m.embeddingStore != nil {
			m.embeddingStore.ClearAll()
		}
		for i := range m.allSessions {
			m.allSessions[i].LLMSummary = ""
		}
		for i := range m.filteredSessions {
			m.filteredSessions[i].LLMSummary = ""
		}
		m.settingsMsg = "Caches cleared (summaries + embeddings)"
		return m, nil
	}
	return m, nil
}

func (m Model) renderSettings() string {
	var b strings.Builder
	b.WriteString(titleStyle.Width(m.width).Render(" claude-manager — Settings"))
	b.WriteString("\n\n")

	// Status indicators
	statusLine := "  "
	if ai.SummaryAvailable() {
		statusLine += lipgloss.NewStyle().Foreground(special).Render("summaries: on")
	} else {
		statusLine += lipgloss.NewStyle().Foreground(dimText).Render("summaries: off")
	}
	statusLine += "  "
	if ai.ResolveOpenAI() != nil {
		statusLine += lipgloss.NewStyle().Foreground(special).Render("embeddings: on")
	} else {
		statusLine += lipgloss.NewStyle().Foreground(dimText).Render("embeddings: off")
	}
	b.WriteString(statusLine)
	b.WriteString("\n\n")

	for i, f := range m.settingsFields {
		label := lipgloss.NewStyle().Foreground(dimText).Width(22).Render(f.Label)

		var value string
		if m.settingsEditing && i == m.settingsCursor {
			value = m.settingsInput.View()
		} else if f.Secret {
			value = lipgloss.NewStyle().Foreground(white).Render(maskSecret(f.Value))
		} else if f.Value == "" {
			value = lipgloss.NewStyle().Foreground(dimText).Render("(not set)")
		} else {
			value = lipgloss.NewStyle().Foreground(white).Render(f.Value)
		}

		line := fmt.Sprintf("%s %s", label, value)
		if i == m.settingsCursor {
			b.WriteString(selectedItemStyle.Render(line))
		} else {
			b.WriteString(itemStyle.Render(line))
		}
		b.WriteString("\n")
	}

	if m.settingsMsg != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(special).Padding(0, 2).Render(m.settingsMsg))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if m.settingsEditing {
		b.WriteString(helpStyle.Render("enter save • esc cancel"))
	} else {
		b.WriteString(helpStyle.Render("↑↓ navigate • enter edit • backspace clear field • c clear caches • Esc back • q quit"))
	}
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
		{"a", "Toggle scope (current dir only vs all sessions)"},
		{"w", "Toggle worktree mode (resume on session's branch in a new worktree)"},
		{"t", "Manage worktrees"},
		{"/", "Search (@repo to filter by project)"},
		{"Tab/Enter", "Confirm search (triggers semantic search in semantic mode)"},
		{"Ctrl+F", "Cycle search mode: quick → full-text → semantic"},
		{"s", "Settings (API keys, providers)"},
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
