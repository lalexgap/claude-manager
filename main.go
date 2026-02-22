package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
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
	case args[0] == "mainenv":
		runMainEnv(args[1:])
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
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv status")
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv request <agent-name> <request-type> [payload-json]")
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv queue")
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv grant-next [mode] [ttl]")
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv release [success|failed] [result-json]")
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv reclaim-stale")
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

func runMainEnv(args []string) {
	store, err := orchestrator.NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: claude-manager mainenv [status | request <agent-name> <request-type> [payload-json] | queue | grant-next [mode] [ttl] | release [success|failed] [result-json] | reclaim-stale]")
		os.Exit(1)
	}

	switch args[0] {
	case "status":
		status, err := store.MainEnvStatus()
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error reading main environment status: %v\n", err)
			os.Exit(1)
		}
		holder := "none"
		if status.Lease.HolderAgentName != "" {
			holder = status.Lease.HolderAgentName
		}
		fmt.Printf("Lease active: %t\n", status.Lease.Active)
		fmt.Printf("Holder: %s\n", holder)
		fmt.Printf("Mode: %s\n", status.Lease.Mode)
		if status.Lease.ExpiresAt != "" {
			fmt.Printf("Expires at: %s\n", status.Lease.ExpiresAt)
		} else {
			fmt.Println("Expires at: -")
		}
		fmt.Printf("Queued requests: %d\n", status.QueueDepth)
	case "request":
		if len(args) < 3 || len(args) > 4 {
			fmt.Fprintln(os.Stderr, "Usage: claude-manager mainenv request <agent-name> <request-type> [payload-json]")
			os.Exit(1)
		}
		payload := ""
		if len(args) == 4 {
			payload = args[3]
		}
		req, err := store.RequestMainEnv(args[1], args[2], payload, "")
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error requesting main environment: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Queued request id=%d for agent %q (%s)\n", req.ID, req.AgentName, req.RequestType)
	case "queue":
		requests, err := store.ListMainEnvQueue()
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error reading main environment queue: %v\n", err)
			os.Exit(1)
		}
		if len(requests) == 0 {
			fmt.Println("Main environment queue is empty.")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tAGENT\tTYPE\tSTATUS\tQUEUED_AT\tSTARTED_AT")
		for _, req := range requests {
			startedAt := req.StartedAt
			if startedAt == "" {
				startedAt = "-"
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", req.ID, req.AgentName, req.RequestType, req.Status, req.QueuedAt, startedAt)
		}
		w.Flush()
	case "grant-next":
		mode := "normal"
		ttl := 10 * time.Minute
		if len(args) >= 2 {
			mode = strings.TrimSpace(args[1])
		}
		if len(args) >= 3 {
			parsedTTL, err := time.ParseDuration(args[2])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Invalid ttl %q: %v\n", args[2], err)
				os.Exit(1)
			}
			ttl = parsedTTL
		}
		if len(args) > 3 {
			fmt.Fprintln(os.Stderr, "Usage: claude-manager mainenv grant-next [mode] [ttl]")
			os.Exit(1)
		}
		if mode != "normal" && mode != "demo" {
			fmt.Fprintln(os.Stderr, "Mode must be one of: normal, demo")
			os.Exit(1)
		}
		req, err := store.GrantNextMainEnv(mode, ttl)
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error granting next main environment request: %v\n", err)
			os.Exit(1)
		}
		if req == nil {
			fmt.Println("No queued main environment requests.")
			return
		}
		fmt.Printf("Granted request id=%d to agent %q in mode=%s ttl=%s\n", req.ID, req.AgentName, mode, ttl)
	case "release":
		markFailed := false
		result := ""
		if len(args) >= 2 {
			switch args[1] {
			case "success":
				markFailed = false
			case "failed":
				markFailed = true
			default:
				fmt.Fprintln(os.Stderr, "Usage: claude-manager mainenv release [success|failed] [result-json]")
				os.Exit(1)
			}
		}
		if len(args) >= 3 {
			result = args[2]
		}
		if len(args) > 3 {
			fmt.Fprintln(os.Stderr, "Usage: claude-manager mainenv release [success|failed] [result-json]")
			os.Exit(1)
		}
		if err := store.ReleaseMainEnv(result, markFailed); err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error releasing main environment lease: %v\n", err)
			os.Exit(1)
		}
		if markFailed {
			fmt.Println("Released main environment lease as failed.")
			return
		}
		fmt.Println("Released main environment lease as success.")
	case "reclaim-stale":
		reclaimed, err := store.ReclaimStaleLease(time.Now().UTC())
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error reclaiming stale lease: %v\n", err)
			os.Exit(1)
		}
		if reclaimed {
			fmt.Println("Reclaimed stale main environment lease.")
			return
		}
		fmt.Println("No stale main environment lease found.")
	default:
		fmt.Fprintln(os.Stderr, "Usage: claude-manager mainenv [status | request <agent-name> <request-type> [payload-json] | queue | grant-next [mode] [ttl] | release [success|failed] [result-json] | reclaim-stale]")
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
