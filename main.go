package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/lalexgap/claude-manager/internal/orchestrator"
	"github.com/lalexgap/claude-manager/internal/sessions"
	"github.com/lalexgap/claude-manager/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	args := os.Args[1:]

	switch {
	case len(args) == 0:
		runTUI()
	case args[0] == "list":
		runList()
	case args[0] == "resume" && len(args) >= 2:
		runResume(args[1])
	case args[0] == "orchestrator":
		runOrchestrator(args[1:])
	case args[0] == "agent":
		runAgent(args[1:])
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  claude-manager")
	fmt.Fprintln(os.Stderr, "  claude-manager list")
	fmt.Fprintln(os.Stderr, "  claude-manager resume <session-id>")
	fmt.Fprintln(os.Stderr, "  claude-manager orchestrator init")
	fmt.Fprintln(os.Stderr, "  claude-manager orchestrator status")
	fmt.Fprintln(os.Stderr, "  claude-manager agent add <name> <workspace>")
	fmt.Fprintln(os.Stderr, "  claude-manager agent list")
}

func loadSessions() []sessions.Session {
	ss, err := sessions.LoadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading sessions: %v\n", err)
		os.Exit(1)
	}
	if len(ss) == 0 {
		fmt.Fprintln(os.Stderr, "No sessions found in ~/.claude/projects/")
		os.Exit(0)
	}
	return ss
}

func runTUI() {
	ss := loadSessions()
	cwd, _ := os.Getwd()
	m := tui.NewModel(ss, cwd)

	p := tea.NewProgram(m, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	final := result.(tui.Model)

	if path := final.NewSessionPath(); path != "" {
		startNewSession(path, final.SkipPermissions, final.UseWorktree)
		return
	}

	selected := final.SelectedSession()
	if selected == nil {
		return
	}

	resumeSession(*selected, final.SkipPermissions, final.UseWorktree)
}

func runList() {
	ss := loadSessions()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSUMMARY\tBRANCH\tLAST ACTIVE\tSESSION ID")
	for _, s := range ss {
		summary := truncateRunes(s.Summary, 60)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			s.Project, summary, s.GitBranch, s.TimeAgo(), s.ID)
	}
	w.Flush()
}

func runResume(sessionID string) {
	ss := loadSessions()

	for _, s := range ss {
		if s.ID == sessionID {
			resumeSession(s, false, false)
			return
		}
	}

	fmt.Fprintf(os.Stderr, "Session not found: %s\n", sessionID)
	os.Exit(1)
}

func runOrchestrator(args []string) {
	store, err := orchestrator.NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: claude-manager orchestrator [init | status]")
		os.Exit(1)
	}

	switch args[0] {
	case "init":
		if err := store.Init(); err != nil {
			fmt.Fprintf(os.Stderr, "Error initializing orchestrator database: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Initialized orchestrator database at %s\n", store.DBPath())
	case "status":
		status, err := store.Status()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading orchestrator status: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Initialized: %t\n", status.Initialized)
		fmt.Printf("DB Path: %s\n", status.DBPath)
		fmt.Printf("Agents: %d\n", status.AgentCount)
		fmt.Printf("Events: %d\n", status.EventCount)
	default:
		fmt.Fprintln(os.Stderr, "Usage: claude-manager orchestrator [init | status]")
		os.Exit(1)
	}
}

func runAgent(args []string) {
	store, err := orchestrator.NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: claude-manager agent [add <name> <workspace> | list]")
		os.Exit(1)
	}

	switch args[0] {
	case "add":
		if len(args) != 3 {
			fmt.Fprintln(os.Stderr, "Usage: claude-manager agent add <name> <workspace>")
			os.Exit(1)
		}
		agent, err := store.AddAgent(args[1], args[2])
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error adding agent: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Added agent %q (id=%d)\n", agent.Name, agent.ID)
	case "list":
		agents, err := store.ListAgents()
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error listing agents: %v\n", err)
			os.Exit(1)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tWORKSPACE\tUPDATED_AT")
		for _, a := range agents {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", a.ID, a.Name, a.Status, a.WorkspacePath, a.UpdatedAt)
		}
		w.Flush()
	default:
		fmt.Fprintln(os.Stderr, "Usage: claude-manager agent [add <name> <workspace> | list]")
		os.Exit(1)
	}
}

func startNewSession(projectPath string, skipPermissions bool, useWorktree bool) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: 'claude' not found in PATH\n")
		os.Exit(1)
	}

	if err := os.Chdir(projectPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error changing to %s: %v\n", projectPath, err)
		os.Exit(1)
	}

	fmt.Printf("Starting new session in %s...\n", projectPath)

	claudeArgs := []string{"claude"}
	if useWorktree {
		claudeArgs = append(claudeArgs, "--worktree")
	}
	if skipPermissions {
		claudeArgs = append(claudeArgs, "--dangerously-skip-permissions")
	}

	err = syscall.Exec(claudePath, claudeArgs, os.Environ())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error exec: %v\n", err)
		os.Exit(1)
	}
}

func resumeSession(s sessions.Session, skipPermissions bool, useWorktree bool) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: 'claude' not found in PATH\n")
		os.Exit(1)
	}

	// Change to the project directory
	if s.ProjectPath != "" {
		if err := os.Chdir(s.ProjectPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error changing to %s: %v\n", s.ProjectPath, err)
			os.Exit(1)
		}
	}

	fmt.Printf("Resuming session in %s...\n", s.ProjectPath)

	// Build claude args
	claudeArgs := []string{"claude", "-r", s.ID}
	if useWorktree {
		claudeArgs = append(claudeArgs, "--worktree")
	}
	if skipPermissions {
		claudeArgs = append(claudeArgs, "--dangerously-skip-permissions")
	}

	// Replace this process with claude -r <session-id>
	err = syscall.Exec(claudePath, claudeArgs, os.Environ())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error exec: %v\n", err)
		os.Exit(1)
	}
}

func truncateRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string([]rune(s)[:maxLen])
	}
	r := []rune(s)
	return string(r[:maxLen-3]) + "..."
}
