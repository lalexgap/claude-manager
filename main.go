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
	"claude-manager/internal/worktree"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// Parse flags: "!" for skip-permissions, "w" for worktree mode
	var skipPerms, useWorktree bool
	var rest []string
	for _, a := range os.Args[1:] {
		switch a {
		case "!":
			skipPerms = true
		case "w":
			useWorktree = true
		default:
			rest = append(rest, a)
		}
	}

	switch {
	case len(rest) == 0:
		runTUI(skipPerms, useWorktree)
	case rest[0] == "n":
		cwd, _ := os.Getwd()
		if useWorktree {
			worktreeNewSession(cwd, skipPerms)
		} else {
			startNewSession(cwd, skipPerms)
		}
	case rest[0] == "list":
		runList()
	case rest[0] == "resume" && len(rest) >= 2:
		runResume(rest[1])
	default:
		fmt.Fprintf(os.Stderr, "Usage: claude-manager [! w] [n | list | resume <session-id>]\n")
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

func runTUI(skipPerms, useWorktree bool) {
	ss := loadSessions()
	cwd, _ := os.Getwd()
	m := tui.NewModel(ss, cwd)
	m.SkipPermissions = skipPerms
	m.UseWorktree = useWorktree

	p := tea.NewProgram(m, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	final := result.(tui.Model)

	if path := final.NewSessionPath(); path != "" {
		if final.UseWorktree {
			worktreeNewSession(path, final.SkipPermissions)
		} else {
			startNewSession(path, final.SkipPermissions)
		}
		return
	}

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

	// If the stored project path no longer exists (e.g. a deleted worktree),
	// resolve gracefully: recreate registered worktrees, or fall back to repo root.
	if _, err := os.Stat(projectPath); os.IsNotExist(err) {
		resolved := worktree.ResolveWorktreePath(projectPath)
		fmt.Fprintf(os.Stderr, "Warning: project directory not found (%s), using %s\n", projectPath, resolved)
		projectPath = resolved
	}

	// Find the main repo root (not a worktree checkout)
	repoRoot := findMainRepoRoot(projectPath)
	if repoRoot == "" {
		fmt.Fprintf(os.Stderr, "Error finding git root for %s\n", projectPath)
		os.Exit(1)
	}

	// Build worktree path: <repoRoot>/.claude/worktrees/<sanitized-branch>
	wtPath := worktree.Path(repoRoot, s.GitBranch)

	// Create worktree if it doesn't exist
	if _, err := os.Stat(wtPath); os.IsNotExist(err) {
		fmt.Printf("Creating worktree at %s for branch %s...\n", wtPath, s.GitBranch)
		wtPath, err = worktree.Create(repoRoot, s.GitBranch)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating worktree: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Printf("Reusing existing worktree at %s\n", wtPath)
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
	worktreeEncoded := strings.ReplaceAll(wtPath, "/", "-")
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
	if err := os.Chdir(wtPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error changing to worktree %s: %v\n", wtPath, err)
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

	fmt.Printf("Resuming session in worktree %s...\n", wtPath)
	err = syscall.Exec(claudePath, claudeArgs, os.Environ())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error exec: %v\n", err)
		os.Exit(1)
	}
}

func worktreeNewSession(projectPath string, skipPermissions bool) {
	// Find the main repo root (not a worktree checkout)
	repoRoot := findMainRepoRoot(projectPath)
	if repoRoot == "" {
		fmt.Fprintf(os.Stderr, "Error: %s is not a git repository\n", projectPath)
		os.Exit(1)
	}

	// Get current branch
	cmd := exec.Command("git", "-C", projectPath, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error detecting branch in %s: %v\n", projectPath, err)
		os.Exit(1)
	}
	branch := strings.TrimSpace(string(out))

	// Compute worktree path: <repoRoot>/.claude/worktrees/<sanitized-branch>
	wtPath := worktree.Path(repoRoot, branch)

	// Create worktree if it doesn't exist
	if _, err := os.Stat(wtPath); os.IsNotExist(err) {
		fmt.Printf("Creating worktree at %s for branch %s...\n", wtPath, branch)
		wtPath, err = worktree.Create(repoRoot, branch)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating worktree: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Printf("Reusing existing worktree at %s\n", wtPath)
	}

	if err := os.Chdir(wtPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error changing to worktree %s: %v\n", wtPath, err)
		os.Exit(1)
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: 'claude' not found in PATH\n")
		os.Exit(1)
	}

	fmt.Printf("Starting new session in worktree %s...\n", wtPath)

	claudeArgs := []string{"claude"}
	if skipPermissions {
		claudeArgs = append(claudeArgs, "--dangerously-skip-permissions")
	}

	err = syscall.Exec(claudePath, claudeArgs, os.Environ())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error exec: %v\n", err)
		os.Exit(1)
	}
}

func startNewSession(projectPath string, skipPermissions bool) {
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
	if skipPermissions {
		claudeArgs = append(claudeArgs, "--dangerously-skip-permissions")
	}

	err = syscall.Exec(claudePath, claudeArgs, os.Environ())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error exec: %v\n", err)
		os.Exit(1)
	}
}

// findMainRepoRoot returns the main repository root, not a worktree checkout root.
// Uses git-common-dir to find the real .git dir, then derives the repo root.
func findMainRepoRoot(path string) string {
	// First get the toplevel (could be a worktree)
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	toplevel := strings.TrimSpace(string(out))

	// Get the common git dir (shared across all worktrees)
	cmd = exec.Command("git", "-C", path, "rev-parse", "--git-common-dir")
	out, err = cmd.Output()
	if err != nil {
		return toplevel
	}
	commonDir := strings.TrimSpace(string(out))

	// If commonDir is absolute and ends in .git, the repo root is its parent
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(toplevel, commonDir)
	}
	commonDir = filepath.Clean(commonDir)

	// .git dir is at <repo>/.git — parent is the repo root
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir)
	}
	return toplevel
}

func resumeSession(s sessions.Session, skipPermissions bool) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: 'claude' not found in PATH\n")
		os.Exit(1)
	}

	// Change to the project directory, resolving missing paths (e.g. deleted worktrees).
	if s.ProjectPath != "" {
		projectPath := s.ProjectPath
		if _, err := os.Stat(projectPath); os.IsNotExist(err) {
			projectPath = worktree.ResolveWorktreePath(projectPath)
			fmt.Fprintf(os.Stderr, "Warning: project directory not found, using %s\n", projectPath)
		}
		if err := os.Chdir(projectPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not change to %s, resuming from current directory\n", projectPath)
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
