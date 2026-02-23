package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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
	fmt.Fprintln(os.Stderr, "  claude-manager agent start <name> [command ...]")
	fmt.Fprintln(os.Stderr, "  claude-manager agent stop <name> [--force]")
	fmt.Fprintln(os.Stderr, "  claude-manager agent status <name>")
	fmt.Fprintln(os.Stderr, "  claude-manager agent heartbeat <name>")
	fmt.Fprintln(os.Stderr, "  claude-manager agent heartbeat-loop <name> [interval]")
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv status")
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv request <agent-name> <request-type> [payload-json]")
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv queue")
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv grant-next [mode] [ttl]")
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv run-next [mode] [ttl]")
	fmt.Fprintln(os.Stderr, "  claude-manager mainenv renew [ttl]")
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
		fmt.Fprintln(os.Stderr, "Usage: claude-manager agent [add <name> <workspace> | list | start <name> [command ...] | stop <name> [--force] | status <name> | heartbeat <name> | heartbeat-loop <name> [interval]]")
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
	case "start":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: claude-manager agent start <name> [command ...]")
			os.Exit(1)
		}
		agentName := args[1]
		command := args[2:]
		if len(command) == 0 {
			command = []string{"claude"}
		}
		agent, err := store.StartAgentProcess(agentName, command)
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error starting agent process: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Started agent %q with session_id=%s\n", agent.Name, agent.SessionID)
	case "stop":
		if len(args) < 2 || len(args) > 3 {
			fmt.Fprintln(os.Stderr, "Usage: claude-manager agent stop <name> [--force]")
			os.Exit(1)
		}
		force := false
		if len(args) == 3 {
			if args[2] != "--force" {
				fmt.Fprintln(os.Stderr, "Usage: claude-manager agent stop <name> [--force]")
				os.Exit(1)
			}
			force = true
		}
		agent, err := store.StopAgentProcess(args[1], force)
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error stopping agent process: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Stopped agent %q (status=%s)\n", agent.Name, agent.Status)
	case "status":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "Usage: claude-manager agent status <name>")
			os.Exit(1)
		}
		agent, pidAlive, err := store.AgentStatus(args[1])
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error reading agent status: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Name: %s\n", agent.Name)
		fmt.Printf("Status: %s\n", agent.Status)
		if agent.SessionID == "" {
			fmt.Println("Session ID: -")
		} else {
			fmt.Printf("Session ID: %s\n", agent.SessionID)
		}
		if _, hasPID := orchestrator.ParseAgentPIDSessionID(agent.SessionID); hasPID {
			fmt.Printf("PID alive: %t\n", pidAlive)
		} else {
			fmt.Println("PID alive: n/a")
		}
		fmt.Printf("Workspace: %s\n", agent.WorkspacePath)
		fmt.Printf("Updated at: %s\n", agent.UpdatedAt)
	case "heartbeat":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "Usage: claude-manager agent heartbeat <name>")
			os.Exit(1)
		}
		agent, err := store.HeartbeatAgent(args[1])
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error updating agent heartbeat: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Heartbeat updated for agent %q at %s\n", agent.Name, agent.LastHeartbeat)
	case "heartbeat-loop":
		if len(args) < 2 || len(args) > 3 {
			fmt.Fprintln(os.Stderr, "Usage: claude-manager agent heartbeat-loop <name> [interval]")
			os.Exit(1)
		}
		interval := 15 * time.Second
		if len(args) == 3 {
			parsed, err := time.ParseDuration(args[2])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Invalid interval %q: %v\n", args[2], err)
				os.Exit(1)
			}
			interval = parsed
		}
		if err := runAgentHeartbeatLoop(store, args[1], interval); err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Heartbeat loop failed: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "Usage: claude-manager agent [add <name> <workspace> | list | start <name> [command ...] | stop <name> [--force] | status <name> | heartbeat <name> | heartbeat-loop <name> [interval]]")
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
		fmt.Fprintln(os.Stderr, "Usage: claude-manager mainenv [status | request <agent-name> <request-type> [payload-json] | queue | grant-next [mode] [ttl] | run-next [mode] [ttl] | renew [ttl] | release [success|failed] [result-json] | reclaim-stale]")
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
		mode, ttl, err := parseMainEnvGrantArgs(args[1:], "grant-next")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
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
	case "run-next":
		mode, ttl, err := parseMainEnvGrantArgs(args[1:], "run-next")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cfg, err := orchestrator.LoadGatewayConfig("")
		if err != nil {
			if errors.Is(err, orchestrator.ErrGatewayConfigNotFound) {
				fmt.Fprintf(os.Stderr, "%v\nCreate the gateway config file and retry.\n", err)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error loading gateway config: %v\n", err)
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

		stopAutoRenew := startMainEnvAutoRenewLoop(store, ttl)
		result := orchestrator.ExecuteMainEnvRequest(*req, cfg)
		autoRenewErr := stopAutoRenew()
		if autoRenewErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: auto-renew loop error: %v\n", autoRenewErr)
		}

		resultJSON, _ := json.Marshal(result)
		releaseErr := store.ReleaseMainEnv(string(resultJSON), !result.Success)
		if releaseErr != nil {
			fmt.Fprintf(os.Stderr, "Request executed but failed to release lease cleanly: %v\n", releaseErr)
			os.Exit(1)
		}

		if result.Success {
			fmt.Printf("Executed request id=%d (%s) for agent %q successfully (exit=%d).\n", req.ID, req.RequestType, req.AgentName, result.ExitCode)
			return
		}
		fmt.Printf("Executed request id=%d (%s) for agent %q but it failed (exit=%d).\n", req.ID, req.RequestType, req.AgentName, result.ExitCode)
		if result.Error != "" {
			fmt.Printf("Error: %s\n", result.Error)
		}
		os.Exit(1)
	case "renew":
		ttl := 10 * time.Minute
		if len(args) >= 2 {
			parsedTTL, err := time.ParseDuration(args[1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Invalid ttl %q: %v\n", args[1], err)
				os.Exit(1)
			}
			ttl = parsedTTL
		}
		if len(args) > 2 {
			fmt.Fprintln(os.Stderr, "Usage: claude-manager mainenv renew [ttl]")
			os.Exit(1)
		}
		lease, err := store.RenewMainEnvLease(ttl)
		if err != nil {
			if errors.Is(err, orchestrator.ErrNotInitialized) {
				fmt.Fprintln(os.Stderr, "Orchestrator database is not initialized. Run: claude-manager orchestrator init")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error renewing main environment lease: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Renewed main environment lease for %q to %s\n", lease.HolderAgentName, lease.ExpiresAt)
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
		fmt.Fprintln(os.Stderr, "Usage: claude-manager mainenv [status | request <agent-name> <request-type> [payload-json] | queue | grant-next [mode] [ttl] | run-next [mode] [ttl] | renew [ttl] | release [success|failed] [result-json] | reclaim-stale]")
		os.Exit(1)
	}
}

func parseMainEnvGrantArgs(args []string, commandName string) (string, time.Duration, error) {
	mode := "normal"
	ttl := 10 * time.Minute

	if len(args) >= 1 {
		mode = strings.TrimSpace(args[0])
	}
	if len(args) >= 2 {
		parsedTTL, err := time.ParseDuration(args[1])
		if err != nil {
			return "", 0, fmt.Errorf("invalid ttl %q: %w", args[1], err)
		}
		ttl = parsedTTL
	}
	if len(args) > 2 {
		return "", 0, fmt.Errorf("usage: claude-manager mainenv %s [mode] [ttl]", commandName)
	}
	if mode != "normal" && mode != "demo" {
		return "", 0, fmt.Errorf("mode must be one of: normal, demo")
	}

	return mode, ttl, nil
}

func runAgentHeartbeatLoop(store *orchestrator.Store, agentName string, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("interval must be greater than zero")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	heartbeat := func() error {
		agent, err := store.HeartbeatAgent(agentName)
		if err != nil {
			return err
		}
		fmt.Printf("[%s] heartbeat updated for %q\n", time.Now().Format(time.RFC3339), agent.Name)
		return nil
	}

	if err := heartbeat(); err != nil {
		return err
	}
	fmt.Printf("Running heartbeat loop for %q every %s (Ctrl+C to stop)\n", agentName, interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := heartbeat(); err != nil {
				return err
			}
		case <-sigCh:
			fmt.Println("Heartbeat loop stopped.")
			return nil
		}
	}
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
