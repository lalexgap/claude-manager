package orchestrator

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

var ErrNotInitialized = errors.New("orchestrator database is not initialized")

type Store struct {
	dbPath string
}

func NewStore() (*Store, error) {
	path, err := DefaultDBPath()
	if err != nil {
		return nil, err
	}
	return &Store{dbPath: path}, nil
}

func (s *Store) DBPath() string {
	return s.dbPath
}

func (s *Store) Init() error {
	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0o755); err != nil {
		return fmt.Errorf("create orchestrator directory: %w", err)
	}

	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite database: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}

	return nil
}

func (s *Store) Status() (Status, error) {
	status := Status{DBPath: s.dbPath}

	if _, err := os.Stat(s.dbPath); errors.Is(err, os.ErrNotExist) {
		return status, nil
	} else if err != nil {
		return status, fmt.Errorf("stat database: %w", err)
	}

	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return status, fmt.Errorf("open sqlite database: %w", err)
	}
	defer db.Close()

	initialized, err := isInitialized(db)
	if err != nil {
		return status, err
	}
	status.Initialized = initialized
	if !initialized {
		return status, nil
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&status.AgentCount); err != nil {
		return status, fmt.Errorf("count agents: %w", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&status.EventCount); err != nil {
		return status, fmt.Errorf("count events: %w", err)
	}

	return status, nil
}

func (s *Store) AddAgent(name, workspace string) (Agent, error) {
	agent := Agent{}
	name = strings.TrimSpace(name)
	if name == "" {
		return agent, fmt.Errorf("agent name is required")
	}

	workspacePath, err := ExpandPath(workspace)
	if err != nil {
		return agent, err
	}

	stat, err := os.Stat(workspacePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return agent, fmt.Errorf("workspace does not exist: %s", workspacePath)
		}
		return agent, fmt.Errorf("read workspace: %w", err)
	}
	if !stat.IsDir() {
		return agent, fmt.Errorf("workspace is not a directory: %s", workspacePath)
	}

	db, err := s.openInitializedDB()
	if err != nil {
		return agent, err
	}
	defer db.Close()

	res, err := db.Exec(`INSERT INTO agents (name, workspace_path) VALUES (?, ?)`, name, workspacePath)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: agents.name") {
			return agent, fmt.Errorf("agent %q already exists", name)
		}
		return agent, fmt.Errorf("insert agent: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return agent, fmt.Errorf("read inserted id: %w", err)
	}

	if err := db.QueryRow(`SELECT id, name, workspace_path, status, IFNULL(session_id, ''), IFNULL(last_heartbeat_at, ''), created_at, updated_at FROM agents WHERE id = ?`, id).Scan(
		&agent.ID,
		&agent.Name,
		&agent.WorkspacePath,
		&agent.Status,
		&agent.SessionID,
		&agent.LastHeartbeat,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	); err != nil {
		return agent, fmt.Errorf("load inserted agent: %w", err)
	}

	return agent, nil
}

func (s *Store) ListAgents() ([]Agent, error) {
	db, err := s.openInitializedDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT id, name, workspace_path, status, IFNULL(session_id, ''), IFNULL(last_heartbeat_at, ''), created_at, updated_at FROM agents ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query agents: %w", err)
	}
	defer rows.Close()

	agents := make([]Agent, 0)
	for rows.Next() {
		var agent Agent
		if err := rows.Scan(
			&agent.ID,
			&agent.Name,
			&agent.WorkspacePath,
			&agent.Status,
			&agent.SessionID,
			&agent.LastHeartbeat,
			&agent.CreatedAt,
			&agent.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		agents = append(agents, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agents: %w", err)
	}

	return agents, nil
}

func (s *Store) openInitializedDB() (*sql.DB, error) {
	if _, err := os.Stat(s.dbPath); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInitialized
	} else if err != nil {
		return nil, fmt.Errorf("stat database: %w", err)
	}

	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	initialized, err := isInitialized(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	if !initialized {
		db.Close()
		return nil, ErrNotInitialized
	}

	return db, nil
}

func isInitialized(db *sql.DB) (bool, error) {
	var agentsTable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'agents'`).Scan(&agentsTable); err != nil {
		return false, fmt.Errorf("check agents table: %w", err)
	}

	var eventsTable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'events'`).Scan(&eventsTable); err != nil {
		return false, fmt.Errorf("check events table: %w", err)
	}

	return agentsTable == 1 && eventsTable == 1, nil
}
