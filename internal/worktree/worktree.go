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

// CollisionAction controls what Create does when the requested branch is already
// checked out in another worktree (including the main repo checkout).
type CollisionAction int

const (
	// CollisionFail returns an error instead of silently picking a different branch.
	CollisionFail CollisionAction = iota
	// CollisionDerive creates a new branch with a unique suffixed name (e.g. "feat-2")
	// based on the requested branch.
	CollisionDerive
	// CollisionNewName creates a new branch using CreateStrategy.NewName based on
	// the requested branch.
	CollisionNewName
)

// CreateStrategy configures how Create handles collisions.
type CreateStrategy struct {
	OnCollision CollisionAction
	NewName     string // used when OnCollision == CollisionNewName
}

// CreateResult describes a successfully created worktree.
type CreateResult struct {
	Path        string // actual worktree path
	Branch      string // branch the worktree is on (may differ from requested)
	DerivedFrom string // original requested branch when Branch was redirected; "" otherwise
}

// ErrBranchInUse is returned by Create (with CollisionFail) when the requested branch
// is already checked out in another worktree.
type ErrBranchInUse struct {
	Branch string
	At     string // worktree path where the branch is checked out
}

func (e *ErrBranchInUse) Error() string {
	return fmt.Sprintf("branch %q is already checked out at %s", e.Branch, e.At)
}

// Create creates a new git worktree at {repoRoot}/.claude/worktrees/{sanitized-branch}
// and runs any WorktreeCreate hooks configured in settings files.
// If base is non-empty, creates `branch` as a new branch off `base`.
// On collision it returns ErrBranchInUse — call CreateWithStrategy to pick a different policy.
func Create(repoRoot, branch, base string) (CreateResult, error) {
	return CreateWithStrategy(repoRoot, branch, base, CreateStrategy{OnCollision: CollisionFail})
}

// CreateWithStrategy is Create but lets the caller decide what happens on collision.
func CreateWithStrategy(repoRoot, branch, base string, strategy CreateStrategy) (CreateResult, error) {
	worktreePath := Path(repoRoot, branch)
	// If a base is provided, create `branch` as a new branch off `base`.
	if base != "" {
		args := []string{"-C", repoRoot, "worktree", "add", "-b", branch, worktreePath, base}
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return CreateResult{}, fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
		}
		runCreateHooks(repoRoot, worktreePath, branch)
		return CreateResult{Path: worktreePath, Branch: branch}, nil
	}
	// If branch doesn't exist yet, create it from HEAD via -b
	if !branchExists(repoRoot, branch) {
		cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", branch, worktreePath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return CreateResult{}, fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
		}
		runCreateHooks(repoRoot, worktreePath, branch)
		return CreateResult{Path: worktreePath, Branch: branch}, nil
	}
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, branch)
	out, err := cmd.CombinedOutput()
	if err == nil {
		runCreateHooks(repoRoot, worktreePath, branch)
		return CreateResult{Path: worktreePath, Branch: branch}, nil
	}
	if !isCollisionOutput(out) {
		return CreateResult{}, fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}

	// Branch is already checked out elsewhere.
	switch strategy.OnCollision {
	case CollisionFail:
		return CreateResult{}, &ErrBranchInUse{Branch: branch, At: BranchCheckedOutAt(repoRoot, branch)}
	case CollisionDerive:
		newBranch := uniqueBranchName(repoRoot, branch)
		return createDerived(repoRoot, newBranch, branch)
	case CollisionNewName:
		if strategy.NewName == "" {
			return CreateResult{}, fmt.Errorf("CollisionNewName requires a non-empty NewName")
		}
		return createDerived(repoRoot, strategy.NewName, branch)
	default:
		return CreateResult{}, fmt.Errorf("unknown collision action: %d", strategy.OnCollision)
	}
}

// createDerived creates a new worktree for `newBranch`, forking from `baseBranch`.
func createDerived(repoRoot, newBranch, baseBranch string) (CreateResult, error) {
	worktreePath := Path(repoRoot, newBranch)
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", newBranch, worktreePath, baseBranch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return CreateResult{}, fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	runCreateHooks(repoRoot, worktreePath, newBranch)
	return CreateResult{Path: worktreePath, Branch: newBranch, DerivedFrom: baseBranch}, nil
}

func isCollisionOutput(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "is already used by worktree") || strings.Contains(s, "is already checked out")
}

// BranchCheckedOutAt returns the worktree path where the given branch is currently
// checked out, or "" if the branch is not checked out anywhere.
func BranchCheckedOutAt(repoRoot, branch string) string {
	for _, wt := range gitWorktrees(repoRoot) {
		if wt.branch == branch {
			return wt.path
		}
	}
	return ""
}

// branchExists returns true if the given local branch exists in repoRoot.
func branchExists(repoRoot, branch string) bool {
	cmd := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

// uniqueBranchName returns a branch name derived from `base` that doesn't collide
// with existing local branches or worktree paths. Tries base-2, base-3, ...
func uniqueBranchName(repoRoot, base string) string {
	existing := make(map[string]bool)
	for _, b := range ListBranches(repoRoot) {
		existing[b] = true
	}
	wtPaths := make(map[string]bool)
	for _, wt := range gitWorktrees(repoRoot) {
		wtPaths[wt.path] = true
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if existing[candidate] {
			continue
		}
		if wtPaths[Path(repoRoot, candidate)] {
			continue
		}
		return candidate
	}
	return fmt.Sprintf("%s-%d", base, 1000)
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
		// Claude Code native WorktreeCreate hook fields
		"cwd":  repoRoot,
		"name": filepath.Base(worktreePath),
		// Additional fields for hooks that want them directly
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
