package orchestrator

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
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

func (s *Store) RequestMainEnv(agentName, requestType, payloadJSON, reason string) (MainEnvRequest, error) {
	req := MainEnvRequest{}
	agentName = strings.TrimSpace(agentName)
	requestType = strings.TrimSpace(requestType)
	if agentName == "" {
		return req, fmt.Errorf("agent name is required")
	}
	if requestType == "" {
		return req, fmt.Errorf("request type is required")
	}
	if requestType != "run_feature_specs" && requestType != "dev_server" {
		return req, fmt.Errorf("unsupported request type %q (allowed: run_feature_specs, dev_server)", requestType)
	}

	db, err := s.openInitializedDB()
	if err != nil {
		return req, err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return req, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	var agentID int64
	if err := tx.QueryRow(`SELECT id FROM agents WHERE name = ?`, agentName).Scan(&agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return req, fmt.Errorf("agent %q not found", agentName)
		}
		return req, fmt.Errorf("load agent: %w", err)
	}

	res, err := tx.Exec(
		`INSERT INTO main_env_requests (agent_id, request_type, payload_json, reason) VALUES (?, ?, ?, ?)`,
		agentID,
		requestType,
		nullIfEmpty(payloadJSON),
		nullIfEmpty(reason),
	)
	if err != nil {
		return req, fmt.Errorf("insert main env request: %w", err)
	}

	reqID, err := res.LastInsertId()
	if err != nil {
		return req, fmt.Errorf("read inserted request id: %w", err)
	}

	if _, err := tx.Exec(`UPDATE agents SET status = 'waiting_mainenv' WHERE id = ?`, agentID); err != nil {
		return req, fmt.Errorf("update agent status: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO events(kind, actor, entity_id) VALUES (?, ?, ?)`, "mainenv.requested", "agent:"+agentName, fmt.Sprintf("main_env_request:%d", reqID)); err != nil {
		return req, fmt.Errorf("insert event: %w", err)
	}

	req, err = loadMainEnvRequestByIDTx(tx, reqID)
	if err != nil {
		return req, err
	}

	if err := tx.Commit(); err != nil {
		return req, fmt.Errorf("commit transaction: %w", err)
	}

	return req, nil
}

func (s *Store) ListMainEnvQueue() ([]MainEnvRequest, error) {
	db, err := s.openInitializedDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT
			r.id,
			r.agent_id,
			a.name,
			r.request_type,
			IFNULL(r.payload_json, ''),
			IFNULL(r.reason, ''),
			r.priority,
			r.status,
			r.queued_at,
			IFNULL(r.started_at, ''),
			IFNULL(r.finished_at, ''),
			IFNULL(r.result_json, '')
		FROM main_env_requests r
		JOIN agents a ON a.id = r.agent_id
		WHERE r.status IN ('queued', 'granted', 'running')
		ORDER BY r.queued_at ASC, r.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query main env queue: %w", err)
	}
	defer rows.Close()

	requests := make([]MainEnvRequest, 0)
	for rows.Next() {
		var req MainEnvRequest
		if err := rows.Scan(
			&req.ID,
			&req.AgentID,
			&req.AgentName,
			&req.RequestType,
			&req.PayloadJSON,
			&req.Reason,
			&req.Priority,
			&req.Status,
			&req.QueuedAt,
			&req.StartedAt,
			&req.FinishedAt,
			&req.ResultJSON,
		); err != nil {
			return nil, fmt.Errorf("scan main env request: %w", err)
		}
		requests = append(requests, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate main env queue: %w", err)
	}

	return requests, nil
}

func (s *Store) MainEnvStatus() (MainEnvStatus, error) {
	status := MainEnvStatus{}

	db, err := s.openInitializedDB()
	if err != nil {
		return status, err
	}
	defer db.Close()

	var isActive int
	if err := db.QueryRow(`
		SELECT
			IFNULL(l.holder_agent_id, 0),
			IFNULL(a.name, ''),
			l.mode,
			IFNULL(l.lease_token, ''),
			IFNULL(l.acquired_at, ''),
			IFNULL(l.expires_at, ''),
			IFNULL(l.last_heartbeat_at, ''),
			CASE
				WHEN l.holder_agent_id IS NOT NULL AND l.expires_at IS NOT NULL AND l.expires_at > CURRENT_TIMESTAMP THEN 1
				ELSE 0
		    END
		FROM main_env_lease l
		LEFT JOIN agents a ON a.id = l.holder_agent_id
		WHERE l.id = 1`).Scan(
		&status.Lease.HolderAgentID,
		&status.Lease.HolderAgentName,
		&status.Lease.Mode,
		&status.Lease.LeaseToken,
		&status.Lease.AcquiredAt,
		&status.Lease.ExpiresAt,
		&status.Lease.LastHeartbeatAt,
		&isActive,
	); err != nil {
		return status, fmt.Errorf("query main env lease: %w", err)
	}
	status.Lease.Active = isActive == 1

	if err := db.QueryRow(`SELECT COUNT(*) FROM main_env_requests WHERE status = 'queued'`).Scan(&status.QueueDepth); err != nil {
		return status, fmt.Errorf("count main env queue: %w", err)
	}

	return status, nil
}

func (s *Store) GrantNextMainEnv(mode string, ttl time.Duration) (*MainEnvRequest, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "normal"
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("ttl must be greater than zero")
	}

	db, err := s.openInitializedDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	var holderAgentID int64
	var leaseExpiresAt string
	var hasActiveLease int
	if err := tx.QueryRow(`
		SELECT
			IFNULL(holder_agent_id, 0),
			IFNULL(expires_at, ''),
			CASE
				WHEN holder_agent_id IS NOT NULL
				 AND expires_at IS NOT NULL
				 AND expires_at > CURRENT_TIMESTAMP THEN 1
				ELSE 0
			END
		FROM main_env_lease
		WHERE id = 1`).Scan(&holderAgentID, &leaseExpiresAt, &hasActiveLease); err != nil {
		return nil, fmt.Errorf("check current lease: %w", err)
	}
	if hasActiveLease > 0 {
		return nil, fmt.Errorf("main environment already has an active lease")
	}
	if holderAgentID != 0 {
		if leaseExpiresAt == "" {
			return nil, fmt.Errorf("main environment has a stale lease; run `claude-manager mainenv reclaim-stale`")
		}
		return nil, fmt.Errorf("main environment has an expired lease (%s); run `claude-manager mainenv reclaim-stale`", leaseExpiresAt)
	}

	req := MainEnvRequest{}
	if err := tx.QueryRow(`
		SELECT
			r.id,
			r.agent_id,
			a.name,
			r.request_type,
			IFNULL(r.payload_json, ''),
			IFNULL(r.reason, ''),
			r.priority,
			r.status,
			r.queued_at,
			IFNULL(r.started_at, ''),
			IFNULL(r.finished_at, ''),
			IFNULL(r.result_json, '')
		FROM main_env_requests r
		JOIN agents a ON a.id = r.agent_id
		WHERE r.status = 'queued'
		ORDER BY r.queued_at ASC, r.id ASC
		LIMIT 1`).Scan(
		&req.ID,
		&req.AgentID,
		&req.AgentName,
		&req.RequestType,
		&req.PayloadJSON,
		&req.Reason,
		&req.Priority,
		&req.Status,
		&req.QueuedAt,
		&req.StartedAt,
		&req.FinishedAt,
		&req.ResultJSON,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("select next queued request: %w", err)
	}

	if _, err := tx.Exec(`UPDATE main_env_requests SET status = 'running', started_at = CURRENT_TIMESTAMP WHERE id = ?`, req.ID); err != nil {
		return nil, fmt.Errorf("mark request running: %w", err)
	}

	seconds := int64(ttl / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	expiryModifier := fmt.Sprintf("+%d seconds", seconds)
	token := uuid.NewString()
	if _, err := tx.Exec(`
		UPDATE main_env_lease
		SET holder_agent_id = ?,
		    mode = ?,
		    lease_token = ?,
		    acquired_at = CURRENT_TIMESTAMP,
		    expires_at = DATETIME(CURRENT_TIMESTAMP, ?),
		    last_heartbeat_at = CURRENT_TIMESTAMP
		WHERE id = 1`,
		req.AgentID,
		mode,
		token,
		expiryModifier,
	); err != nil {
		return nil, fmt.Errorf("update main env lease: %w", err)
	}

	if _, err := tx.Exec(`UPDATE agents SET status = 'holding_mainenv' WHERE id = ?`, req.AgentID); err != nil {
		return nil, fmt.Errorf("update agent status: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO events(kind, actor, entity_id) VALUES (?, ?, ?)`, "mainenv.granted", "system", fmt.Sprintf("main_env_request:%d", req.ID)); err != nil {
		return nil, fmt.Errorf("insert event: %w", err)
	}

	req, err = loadMainEnvRequestByIDTx(tx, req.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return &req, nil
}

func (s *Store) ReleaseMainEnv(resultJSON string, markFailed bool) error {
	db, err := s.openInitializedDB()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	released, err := releaseMainEnvTx(tx, resultJSON, markFailed)
	if err != nil {
		return err
	}
	if !released {
		return tx.Commit()
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (s *Store) ReclaimStaleLease(now time.Time) (bool, error) {
	db, err := s.openInitializedDB()
	if err != nil {
		return false, err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	var isStale int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM main_env_lease
		WHERE id = 1
		  AND holder_agent_id IS NOT NULL
		  AND expires_at IS NOT NULL
		  AND expires_at <= DATETIME(?)`,
		now.UTC().Format("2006-01-02 15:04:05"),
	).Scan(&isStale); err != nil {
		return false, fmt.Errorf("check stale lease: %w", err)
	}
	if isStale == 0 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit transaction: %w", err)
		}
		return false, nil
	}

	released, err := releaseMainEnvTx(tx, `{"reason":"lease_expired"}`, true)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit transaction: %w", err)
	}
	return released, nil
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

	var mainEnvRequestsTable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'main_env_requests'`).Scan(&mainEnvRequestsTable); err != nil {
		return false, fmt.Errorf("check main_env_requests table: %w", err)
	}

	var mainEnvLeaseTable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'main_env_lease'`).Scan(&mainEnvLeaseTable); err != nil {
		return false, fmt.Errorf("check main_env_lease table: %w", err)
	}

	return agentsTable == 1 && eventsTable == 1 && mainEnvRequestsTable == 1 && mainEnvLeaseTable == 1, nil
}

func loadMainEnvRequestByIDTx(tx *sql.Tx, requestID int64) (MainEnvRequest, error) {
	req := MainEnvRequest{}
	if err := tx.QueryRow(`
		SELECT
			r.id,
			r.agent_id,
			a.name,
			r.request_type,
			IFNULL(r.payload_json, ''),
			IFNULL(r.reason, ''),
			r.priority,
			r.status,
			r.queued_at,
			IFNULL(r.started_at, ''),
			IFNULL(r.finished_at, ''),
			IFNULL(r.result_json, '')
		FROM main_env_requests r
		JOIN agents a ON a.id = r.agent_id
		WHERE r.id = ?`,
		requestID,
	).Scan(
		&req.ID,
		&req.AgentID,
		&req.AgentName,
		&req.RequestType,
		&req.PayloadJSON,
		&req.Reason,
		&req.Priority,
		&req.Status,
		&req.QueuedAt,
		&req.StartedAt,
		&req.FinishedAt,
		&req.ResultJSON,
	); err != nil {
		return req, fmt.Errorf("load main env request: %w", err)
	}
	return req, nil
}

func releaseMainEnvTx(tx *sql.Tx, resultJSON string, markFailed bool) (bool, error) {
	var holderAgentID int64
	if err := tx.QueryRow(`SELECT IFNULL(holder_agent_id, 0) FROM main_env_lease WHERE id = 1`).Scan(&holderAgentID); err != nil {
		return false, fmt.Errorf("query main env lease holder: %w", err)
	}
	if holderAgentID == 0 {
		return false, nil
	}

	var requestID int64
	err := tx.QueryRow(`
		SELECT id
		FROM main_env_requests
		WHERE agent_id = ? AND status = 'running'
		ORDER BY started_at DESC, id DESC
		LIMIT 1`,
		holderAgentID,
	).Scan(&requestID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("query running main env request: %w", err)
	}
	if err == nil {
		finalStatus := "completed"
		if markFailed {
			finalStatus = "failed"
		}
		if _, err := tx.Exec(
			`UPDATE main_env_requests SET status = ?, finished_at = CURRENT_TIMESTAMP, result_json = ? WHERE id = ?`,
			finalStatus,
			nullIfEmpty(resultJSON),
			requestID,
		); err != nil {
			return false, fmt.Errorf("finish main env request: %w", err)
		}
	}

	if _, err := tx.Exec(`UPDATE agents SET status = 'working' WHERE id = ?`, holderAgentID); err != nil {
		return false, fmt.Errorf("update agent status: %w", err)
	}

	if _, err := tx.Exec(`
		UPDATE main_env_lease
		SET holder_agent_id = NULL,
		    mode = 'normal',
		    lease_token = NULL,
		    acquired_at = NULL,
		    expires_at = NULL,
		    last_heartbeat_at = NULL
		WHERE id = 1`); err != nil {
		return false, fmt.Errorf("clear main env lease: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO events(kind, actor, entity_id) VALUES (?, ?, ?)`, "mainenv.released", "system", fmt.Sprintf("agent:%d", holderAgentID)); err != nil {
		return false, fmt.Errorf("insert event: %w", err)
	}

	return true, nil
}

func nullIfEmpty(v string) any {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return v
}
