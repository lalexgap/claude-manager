package orchestrator

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func ParseAgentPIDSessionID(sessionID string) (int, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if !strings.HasPrefix(sessionID, "pid:") {
		return 0, false
	}
	pidText := strings.TrimSpace(strings.TrimPrefix(sessionID, "pid:"))
	if pidText == "" {
		return 0, false
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func ParseAgentTmuxSessionID(sessionID string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if !strings.HasPrefix(sessionID, "tmux:") {
		return "", false
	}
	tmuxName := strings.TrimSpace(strings.TrimPrefix(sessionID, "tmux:"))
	if tmuxName == "" {
		return "", false
	}
	return tmuxName, true
}

func isPIDAlive(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, fmt.Errorf("check pid %d: %w", pid, err)
}

func isTmuxSessionAlive(sessionName string) (bool, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return false, nil
	}
	cmd := exec.Command("tmux", "has-session", "-t", sessionName)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, fmt.Errorf("check tmux session %q: %w", sessionName, err)
}

func tmuxSessionNameForAgent(agent Agent) string {
	safe := sanitizeSessionPart(agent.Name)
	if safe == "" {
		safe = fmt.Sprintf("agent-%d", agent.ID)
	}
	return fmt.Sprintf("cm-%s-%d", safe, agent.ID)
}

func sanitizeSessionPart(raw string) string {
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

func shellJoinCommand(args []string) string {
	if len(args) == 0 {
		return "claude"
	}
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(v string) string {
	if v == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'"
}

func (s *Store) StartAgentTmux(name string, command []string) (Agent, error) {
	agent := Agent{}
	name = strings.TrimSpace(name)
	if name == "" {
		return agent, fmt.Errorf("agent name is required")
	}
	if len(command) == 0 {
		command = []string{"claude"}
	}
	if strings.TrimSpace(command[0]) == "" {
		return agent, fmt.Errorf("command is required")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return agent, fmt.Errorf("tmux is required but not found in PATH")
	}

	db, err := s.openInitializedDB()
	if err != nil {
		return agent, err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return agent, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	agent, err = loadAgentByNameTx(tx, name)
	if err != nil {
		return agent, err
	}

	if pid, hasPID := ParseAgentPIDSessionID(agent.SessionID); hasPID {
		alive, err := isPIDAlive(pid)
		if err != nil {
			return agent, err
		}
		if alive {
			return agent, fmt.Errorf("agent %q is already running with pid %d", name, pid)
		}
		if _, err := tx.Exec(`UPDATE agents SET status = 'idle', session_id = NULL WHERE id = ?`, agent.ID); err != nil {
			return agent, fmt.Errorf("normalize stale agent runtime state: %w", err)
		}
	}
	if existingTmux, hasTmux := ParseAgentTmuxSessionID(agent.SessionID); hasTmux {
		alive, err := isTmuxSessionAlive(existingTmux)
		if err != nil {
			return agent, err
		}
		if alive {
			return agent, fmt.Errorf("agent %q is already running in tmux session %q", name, existingTmux)
		}
		if _, err := tx.Exec(`UPDATE agents SET status = 'idle', session_id = NULL WHERE id = ?`, agent.ID); err != nil {
			return agent, fmt.Errorf("normalize stale agent tmux state: %w", err)
		}
	}

	stat, err := os.Stat(agent.WorkspacePath)
	if err != nil {
		return agent, fmt.Errorf("read agent workspace: %w", err)
	}
	if !stat.IsDir() {
		return agent, fmt.Errorf("agent workspace is not a directory: %s", agent.WorkspacePath)
	}

	tmuxName := tmuxSessionNameForAgent(agent)
	alive, err := isTmuxSessionAlive(tmuxName)
	if err != nil {
		return agent, err
	}
	if alive {
		return agent, fmt.Errorf("tmux session %q already exists", tmuxName)
	}

	shellCommand := shellJoinCommand(command)
	startCmd := exec.Command("tmux", "new-session", "-d", "-s", tmuxName, "-c", agent.WorkspacePath, shellCommand)
	if out, err := startCmd.CombinedOutput(); err != nil {
		return agent, fmt.Errorf("start tmux session %q: %w (%s)", tmuxName, err, strings.TrimSpace(string(out)))
	}

	if _, err := tx.Exec(`UPDATE agents SET status = 'running', session_id = ? WHERE id = ?`, fmt.Sprintf("tmux:%s", tmuxName), agent.ID); err != nil {
		return agent, fmt.Errorf("update agent runtime state: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO events(kind, actor, entity_id) VALUES (?, ?, ?)`, "agent.start", "system", fmt.Sprintf("agent:%d", agent.ID)); err != nil {
		return agent, fmt.Errorf("insert start event: %w", err)
	}

	agent, err = loadAgentByIDTx(tx, agent.ID)
	if err != nil {
		return agent, err
	}

	if err := tx.Commit(); err != nil {
		return agent, fmt.Errorf("commit transaction: %w", err)
	}

	return agent, nil
}

func (s *Store) StartAgentProcess(name string, command []string) (Agent, error) {
	agent := Agent{}
	name = strings.TrimSpace(name)
	if name == "" {
		return agent, fmt.Errorf("agent name is required")
	}
	if len(command) == 0 {
		command = []string{"claude"}
	}
	if strings.TrimSpace(command[0]) == "" {
		return agent, fmt.Errorf("command is required")
	}

	db, err := s.openInitializedDB()
	if err != nil {
		return agent, err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return agent, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	agent, err = loadAgentByNameTx(tx, name)
	if err != nil {
		return agent, err
	}

	if pid, hasPID := ParseAgentPIDSessionID(agent.SessionID); hasPID {
		alive, err := isPIDAlive(pid)
		if err != nil {
			return agent, err
		}
		if alive {
			return agent, fmt.Errorf("agent %q is already running with pid %d", name, pid)
		}
		if _, err := tx.Exec(`UPDATE agents SET status = 'idle', session_id = NULL WHERE id = ?`, agent.ID); err != nil {
			return agent, fmt.Errorf("normalize stale agent runtime state: %w", err)
		}
	}
	if tmuxName, hasTmux := ParseAgentTmuxSessionID(agent.SessionID); hasTmux {
		alive, err := isTmuxSessionAlive(tmuxName)
		if err != nil {
			return agent, err
		}
		if alive {
			return agent, fmt.Errorf("agent %q is already running in tmux session %q", name, tmuxName)
		}
		if _, err := tx.Exec(`UPDATE agents SET status = 'idle', session_id = NULL WHERE id = ?`, agent.ID); err != nil {
			return agent, fmt.Errorf("normalize stale agent tmux state: %w", err)
		}
	}

	stat, err := os.Stat(agent.WorkspacePath)
	if err != nil {
		return agent, fmt.Errorf("read agent workspace: %w", err)
	}
	if !stat.IsDir() {
		return agent, fmt.Errorf("agent workspace is not a directory: %s", agent.WorkspacePath)
	}

	logsDir, err := DefaultAgentsLogsDir()
	if err != nil {
		return agent, err
	}
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return agent, fmt.Errorf("create agents logs directory: %w", err)
	}

	logPath := filepath.Join(logsDir, agent.Name+".log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return agent, fmt.Errorf("open agent log file: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = agent.WorkspacePath
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return agent, fmt.Errorf("start agent process: %w", err)
	}

	pid := cmd.Process.Pid
	_ = cmd.Process.Release()

	if _, err := tx.Exec(`UPDATE agents SET status = 'running', session_id = ? WHERE id = ?`, fmt.Sprintf("pid:%d", pid), agent.ID); err != nil {
		return agent, fmt.Errorf("update agent runtime state: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO events(kind, actor, entity_id) VALUES (?, ?, ?)`, "agent.start", "system", fmt.Sprintf("agent:%d", agent.ID)); err != nil {
		return agent, fmt.Errorf("insert start event: %w", err)
	}

	agent, err = loadAgentByIDTx(tx, agent.ID)
	if err != nil {
		return agent, err
	}

	if err := tx.Commit(); err != nil {
		return agent, fmt.Errorf("commit transaction: %w", err)
	}

	return agent, nil
}

func (s *Store) StopAgentProcess(name string, force bool) (Agent, error) {
	agent := Agent{}
	name = strings.TrimSpace(name)
	if name == "" {
		return agent, fmt.Errorf("agent name is required")
	}

	db, err := s.openInitializedDB()
	if err != nil {
		return agent, err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return agent, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	agent, err = loadAgentByNameTx(tx, name)
	if err != nil {
		return agent, err
	}

	if pid, hasPID := ParseAgentPIDSessionID(agent.SessionID); hasPID {
		alive, err := isPIDAlive(pid)
		if err != nil {
			return agent, err
		}
		if alive {
			sig := syscall.SIGTERM
			if force {
				sig = syscall.SIGKILL
			}
			if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
				return agent, fmt.Errorf("send signal to pid %d: %w", pid, err)
			}
		}
	}
	if tmuxName, hasTmux := ParseAgentTmuxSessionID(agent.SessionID); hasTmux {
		alive, err := isTmuxSessionAlive(tmuxName)
		if err != nil {
			return agent, err
		}
		if alive {
			cmd := exec.Command("tmux", "kill-session", "-t", tmuxName)
			if out, err := cmd.CombinedOutput(); err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					return agent, fmt.Errorf("stop tmux session %q: %w", tmuxName, err)
				}
				trimmed := strings.TrimSpace(string(out))
				if trimmed != "" {
					return agent, fmt.Errorf("stop tmux session %q: %s", tmuxName, trimmed)
				}
			}
		}
	}

	if _, err := tx.Exec(`UPDATE agents SET status = 'idle', session_id = NULL WHERE id = ?`, agent.ID); err != nil {
		return agent, fmt.Errorf("update agent runtime state: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO events(kind, actor, entity_id) VALUES (?, ?, ?)`, "agent.stop", "system", fmt.Sprintf("agent:%d", agent.ID)); err != nil {
		return agent, fmt.Errorf("insert stop event: %w", err)
	}

	agent, err = loadAgentByIDTx(tx, agent.ID)
	if err != nil {
		return agent, err
	}

	if err := tx.Commit(); err != nil {
		return agent, fmt.Errorf("commit transaction: %w", err)
	}

	return agent, nil
}

func (s *Store) AgentStatus(name string) (Agent, bool, error) {
	agent := Agent{}
	name = strings.TrimSpace(name)
	if name == "" {
		return agent, false, fmt.Errorf("agent name is required")
	}

	db, err := s.openInitializedDB()
	if err != nil {
		return agent, false, err
	}
	defer db.Close()

	if err := db.QueryRow(`SELECT id, name, workspace_path, status, IFNULL(session_id, ''), IFNULL(last_heartbeat_at, ''), created_at, updated_at FROM agents WHERE name = ?`, name).Scan(
		&agent.ID,
		&agent.Name,
		&agent.WorkspacePath,
		&agent.Status,
		&agent.SessionID,
		&agent.LastHeartbeat,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return agent, false, fmt.Errorf("agent %q not found", name)
		}
		return agent, false, fmt.Errorf("load agent: %w", err)
	}

	if pid, hasPID := ParseAgentPIDSessionID(agent.SessionID); hasPID {
		alive, err := isPIDAlive(pid)
		if err != nil {
			return agent, false, err
		}
		return agent, alive, nil
	}
	if tmuxName, hasTmux := ParseAgentTmuxSessionID(agent.SessionID); hasTmux {
		alive, err := isTmuxSessionAlive(tmuxName)
		if err != nil {
			return agent, false, err
		}
		return agent, alive, nil
	}

	return agent, false, nil
}

func loadAgentByNameTx(tx *sql.Tx, name string) (Agent, error) {
	agent := Agent{}
	if err := tx.QueryRow(`SELECT id, name, workspace_path, status, IFNULL(session_id, ''), IFNULL(last_heartbeat_at, ''), created_at, updated_at FROM agents WHERE name = ?`, name).Scan(
		&agent.ID,
		&agent.Name,
		&agent.WorkspacePath,
		&agent.Status,
		&agent.SessionID,
		&agent.LastHeartbeat,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return agent, fmt.Errorf("agent %q not found", name)
		}
		return agent, fmt.Errorf("load agent: %w", err)
	}
	return agent, nil
}

func loadAgentByIDTx(tx *sql.Tx, id int64) (Agent, error) {
	agent := Agent{}
	if err := tx.QueryRow(`SELECT id, name, workspace_path, status, IFNULL(session_id, ''), IFNULL(last_heartbeat_at, ''), created_at, updated_at FROM agents WHERE id = ?`, id).Scan(
		&agent.ID,
		&agent.Name,
		&agent.WorkspacePath,
		&agent.Status,
		&agent.SessionID,
		&agent.LastHeartbeat,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return agent, fmt.Errorf("agent with id %d not found", id)
		}
		return agent, fmt.Errorf("load agent: %w", err)
	}
	return agent, nil
}
