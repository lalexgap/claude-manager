package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"claude-manager/internal/sessions"
)

// Entry represents a single git worktree directory.
type Entry struct {
	Path     string // e.g. /Users/x/code/myrepo-worktrees/feature-foo
	Branch   string // directory name (sanitized branch)
	RepoRoot string // e.g. /Users/x/code/myrepo
}

// Discover collects worktree entries across all unique repo roots from sessions.
// Only worktrees registered with git (via git worktree list) are returned.
func Discover(ss []sessions.Session) []Entry {
	roots := uniqueRepoRoots(ss)
	var entries []Entry
	for _, root := range roots {
		wtDir := root + "-worktrees"
		for _, wt := range gitWorktrees(root) {
			if !strings.HasPrefix(wt.path, wtDir+string(filepath.Separator)) {
				continue
			}
			entries = append(entries, Entry{
				Path:     wt.path,
				Branch:   filepath.Base(wt.path),
				RepoRoot: root,
			})
		}
	}
	return entries
}

type gitWorktree struct {
	path string
}

// gitWorktrees returns all worktrees registered with git for the given repo root.
func gitWorktrees(repoRoot string) []gitWorktree {
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var wts []gitWorktree
	for _, line := range strings.Split(string(out), "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			wts = append(wts, gitWorktree{path: strings.TrimSpace(path)})
		}
	}
	return wts
}

// Remove removes a worktree via git and cleans up the Claude session directory.
func Remove(e Entry) error {
	cmd := exec.Command("git", "-C", e.RepoRoot, "worktree", "remove", e.Path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}

	// Clean up ~/.claude/projects/<encoded-path>/
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil // worktree removed, cleanup is best-effort
	}
	encoded := strings.ReplaceAll(e.Path, "/", "-")
	projectDir := filepath.Join(homeDir, ".claude", "projects", encoded)
	if _, err := os.Stat(projectDir); err == nil {
		os.RemoveAll(projectDir)
	}
	return nil
}

// uniqueRepoRoots returns deduplicated git repo roots from session project paths.
func uniqueRepoRoots(ss []sessions.Session) []string {
	seen := make(map[string]bool)
	var roots []string
	for _, s := range ss {
		if s.ProjectPath == "" {
			continue
		}
		cmd := exec.Command("git", "-C", s.ProjectPath, "rev-parse", "--show-toplevel")
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		root := strings.TrimSpace(string(out))
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	return roots
}
