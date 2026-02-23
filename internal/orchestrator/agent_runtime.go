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
