package worktree

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"claude-manager/internal/sessions"
)

// Entry represents a single git worktree directory.
type Entry struct {
	Path       string            // e.g. /Users/x/code/myrepo/.claude/worktrees/feature-foo
	Branch     string            // actual git branch name
	RepoRoot   string            // e.g. /Users/x/code/myrepo
	SessionID  string            // associated Claude session ID, if any
	SessionMsg string            // first line of associated session, if any
	Session    *sessions.Session // full session data, if associated
}

// Path computes the worktree path for a given repo and branch without creating it.
func Path(repoRoot, branch string) string {
	dirName := strings.ReplaceAll(branch, "/", "-")
	return filepath.Join(repoRoot, ".claude", "worktrees", dirName)
}

// Discover collects worktree entries across all unique repo roots from sessions.
// Only worktrees registered with git (via git worktree list) are returned.
// Supports both .claude/worktrees/ (new) and {repo}-worktrees/ (legacy) layouts.
// Correlates each worktree with its most recent Claude session, if any.
func Discover(ss []sessions.Session) []Entry {
	// Build map: projectPath -> most recent session
	sessionByPath := make(map[string]sessions.Session)
	for _, s := range ss {
		if s.ProjectPath == "" {
			continue
		}
		if existing, ok := sessionByPath[s.ProjectPath]; !ok || s.LastActive.After(existing.LastActive) {
			sessionByPath[s.ProjectPath] = s
		}
	}

	roots := uniqueRepoRoots(ss)
	var entries []Entry
	for _, root := range roots {
		newDir := filepath.Join(root, ".claude", "worktrees")
		oldDir := root + "-worktrees"
		sep := string(filepath.Separator)
		for _, wt := range gitWorktrees(root) {
			if !strings.HasPrefix(wt.path, newDir+sep) && !strings.HasPrefix(wt.path, oldDir+sep) {
				continue
			}
			branch := wt.branch
			if branch == "" {
				branch = filepath.Base(wt.path)
			}
			e := Entry{
				Path:     wt.path,
				Branch:   branch,
				RepoRoot: root,
			}
			if sess, ok := sessionByPath[wt.path]; ok {
				e.SessionID = sess.ID
				e.SessionMsg = sess.Summary
				if sess.LLMSummary != "" {
					e.SessionMsg = sess.LLMSummary
				}
				s := sess
				e.Session = &s
			}
			entries = append(entries, e)
		}
	}
	return entries
}

type gitWorktree struct {
	path   string
	branch string // empty if detached HEAD
}

// gitWorktrees returns all worktrees registered with git for the given repo root.
func gitWorktrees(repoRoot string) []gitWorktree {
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var wts []gitWorktree
	for _, block := range strings.Split(string(out), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var path, branch string
		for _, line := range strings.Split(block, "\n") {
			if p, ok := strings.CutPrefix(line, "worktree "); ok {
				path = strings.TrimSpace(p)
			} else if b, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
				branch = strings.TrimSpace(b)
			}
		}
		if path != "" {
			wts = append(wts, gitWorktree{path: path, branch: branch})
		}
	}
	return wts
}

// Create creates a new git worktree at {repoRoot}/.claude/worktrees/{sanitized-branch}
// and runs any WorktreeCreate hooks configured in settings files.
func Create(repoRoot, branch string) (string, error) {
	worktreePath := Path(repoRoot, branch)
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	runCreateHooks(repoRoot, worktreePath, branch)
	return worktreePath, nil
}

// Remove removes a worktree via git and cleans up the Claude session directory.
// If force is true, passes --force to git worktree remove.
func Remove(e Entry, force bool) error {
	args := []string{"-C", e.RepoRoot, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, e.Path)
	cmd := exec.Command("git", args...)
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

// ResolveWorktreePath resolves a valid path for a session whose worktree directory may be missing.
// It walks up ancestor directories to find the git repo, then:
//  1. If git still has the worktree registered (directory deleted without git worktree remove),
//     recreates the worktree directory via git worktree add.
//  2. Otherwise, creates the bare directory so Claude can find the session via --resume.
//  3. Falls back to the repo root if directory creation fails.
func ResolveWorktreePath(missingPath string) string {
	// Find nearest ancestor that exists and belongs to a git repo
	var repoRoot string
	dir := filepath.Dir(missingPath)
	for {
		cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
		if out, err := cmd.Output(); err == nil {
			repoRoot = strings.TrimSpace(string(out))
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if repoRoot == "" {
		return missingPath
	}

	// Check if git still knows about this worktree (directory was manually deleted)
	for _, wt := range gitWorktrees(repoRoot) {
		if wt.path == missingPath && wt.branch != "" {
			cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", missingPath, wt.branch)
			if err := cmd.Run(); err == nil {
				return missingPath
			}
		}
	}

	// Worktree was properly removed — recreate the directory so Claude can find the
	// session via --resume (Claude looks up sessions by encoded cwd path).
	if err := os.MkdirAll(missingPath, 0755); err == nil {
		return missingPath
	}

	return repoRoot
}

// ListBranches returns local branch names for the given repo root.
func ListBranches(repoRoot string) []string {
	cmd := exec.Command("git", "-C", repoRoot, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches
}

func runCreateHooks(repoRoot, worktreePath, branch string) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	settingsPaths := []string{
		filepath.Join(homeDir, ".claude", "settings.json"),
		filepath.Join(repoRoot, ".claude", "settings.json"),
	}
	var hooks []string
	for _, p := range settingsPaths {
		hooks = append(hooks, readHookCommands(p)...)
	}
	if len(hooks) == 0 {
		return
	}
	input, _ := json.Marshal(map[string]string{
		"worktreePath": worktreePath,
		"branch":       branch,
		"repoPath":     repoRoot,
	})
	for _, h := range hooks {
		cmd := exec.Command("sh", "-c", h)
		cmd.Dir = worktreePath
		cmd.Stdin = strings.NewReader(string(input))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run() // best-effort
	}
}

func readHookCommands(settingsPath string) []string {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}
	var cmds []string
	for _, entry := range settings.Hooks["WorktreeCreate"] {
		for _, h := range entry.Hooks {
			if h.Type == "command" && h.Command != "" {
				cmds = append(cmds, h.Command)
			}
		}
	}
	return cmds
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
