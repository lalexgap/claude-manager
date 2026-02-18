package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"claude-manager/internal/sessions"
	"claude-manager/internal/tui"

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
	default:
		fmt.Fprintf(os.Stderr, "Usage: claude-manager [list | resume <session-id>]\n")
		os.Exit(1)
	}
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
	m := tui.NewModel(ss)

	p := tea.NewProgram(m, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	final := result.(tui.Model)
	selected := final.SelectedSession()
	if selected == nil {
		return
	}

	if final.UseWorktree {
		worktreeResume(*selected, final.SkipPermissions)
	} else {
		resumeSession(*selected, final.SkipPermissions)
	}
}

func runList() {
	ss := loadSessions()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSUMMARY\tBRANCH\tLAST ACTIVE\tSESSION ID")
	for _, s := range ss {
		summary := s.Summary
		if len(summary) > 60 {
			summary = summary[:57] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			s.Project, summary, s.GitBranch, s.TimeAgo(), s.ID)
	}
	w.Flush()
}

func runResume(sessionID string) {
	ss := loadSessions()

	for _, s := range ss {
		if s.ID == sessionID {
			resumeSession(s, false)
			return
		}
	}

	fmt.Fprintf(os.Stderr, "Session not found: %s\n", sessionID)
	os.Exit(1)
}

func worktreeResume(s sessions.Session, skipPermissions bool) {
	if s.GitBranch == "" {
		fmt.Fprintln(os.Stderr, "Error: session has no git branch — cannot create worktree")
		os.Exit(1)
	}

	projectPath := s.ProjectPath
	if projectPath == "" {
		fmt.Fprintln(os.Stderr, "Error: session has no project path")
		os.Exit(1)
	}

	// Find git repo root
	cmd := exec.Command("git", "-C", projectPath, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding git root for %s: %v\n", projectPath, err)
		os.Exit(1)
	}
	repoRoot := strings.TrimSpace(string(out))

	// Build worktree path: <repoRoot>-worktrees/<sanitized-branch>/
	sanitizedBranch := strings.ReplaceAll(s.GitBranch, "/", "-")
	worktreePath := filepath.Join(repoRoot+"-worktrees", sanitizedBranch)

	// Create worktree if it doesn't exist
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		fmt.Printf("Creating worktree at %s for branch %s...\n", worktreePath, s.GitBranch)
		cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "-f", worktreePath, s.GitBranch)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating worktree: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Printf("Reusing existing worktree at %s\n", worktreePath)
	}

	// Symlink the session file so Claude can find it from the worktree path.
	// Claude stores sessions in ~/.claude/projects/<encoded-path>/ where the
	// encoded path replaces "/" with "-". The worktree has a different path
	// than the original repo, so we need to link the session file over.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding home dir: %v\n", err)
		os.Exit(1)
	}
	worktreeEncoded := strings.ReplaceAll(worktreePath, "/", "-")
	worktreeProjectDir := filepath.Join(homeDir, ".claude", "projects", worktreeEncoded)
	if err := os.MkdirAll(worktreeProjectDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating project dir: %v\n", err)
		os.Exit(1)
	}
	sessionFileName := filepath.Base(s.FilePath)
	symlinkPath := filepath.Join(worktreeProjectDir, sessionFileName)
	if _, err := os.Lstat(symlinkPath); os.IsNotExist(err) {
		os.Symlink(s.FilePath, symlinkPath)
	}

	// Chdir into worktree and exec claude
	if err := os.Chdir(worktreePath); err != nil {
		fmt.Fprintf(os.Stderr, "Error changing to worktree %s: %v\n", worktreePath, err)
		os.Exit(1)
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: 'claude' not found in PATH\n")
		os.Exit(1)
	}

	claudeArgs := []string{"claude", "-r", s.ID}
	if skipPermissions {
		claudeArgs = append(claudeArgs, "--dangerously-skip-permissions")
	}

	fmt.Printf("Resuming session in worktree %s...\n", worktreePath)
	err = syscall.Exec(claudePath, claudeArgs, os.Environ())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error exec: %v\n", err)
		os.Exit(1)
	}
}

func resumeSession(s sessions.Session, skipPermissions bool) {
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
