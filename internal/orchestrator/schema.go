package orchestrator

const schemaSQL = `
CREATE TABLE IF NOT EXISTS agents (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    workspace_path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'idle',
    session_id TEXT,
    last_heartbeat_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    actor TEXT NOT NULL,
    entity_id TEXT,
    payload_json TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS main_env_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id INTEGER NOT NULL,
    request_type TEXT NOT NULL,
    payload_json TEXT,
    reason TEXT,
    priority TEXT NOT NULL DEFAULT 'normal',
    status TEXT NOT NULL DEFAULT 'queued',
    queued_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME,
    finished_at DATETIME,
    result_json TEXT,
    FOREIGN KEY(agent_id) REFERENCES agents(id)
);

CREATE INDEX IF NOT EXISTS idx_main_env_requests_status_queued_at
ON main_env_requests(status, queued_at);

CREATE INDEX IF NOT EXISTS idx_main_env_requests_agent_id
ON main_env_requests(agent_id);

CREATE TABLE IF NOT EXISTS main_env_lease (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    holder_agent_id INTEGER,
    mode TEXT NOT NULL DEFAULT 'normal',
    lease_token TEXT,
    acquired_at DATETIME,
    expires_at DATETIME,
    last_heartbeat_at DATETIME,
    FOREIGN KEY(holder_agent_id) REFERENCES agents(id)
);

INSERT OR IGNORE INTO main_env_lease(id, mode) VALUES (1, 'normal');

CREATE TRIGGER IF NOT EXISTS trg_agents_updated_at
AFTER UPDATE ON agents
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE agents
    SET updated_at = CURRENT_TIMESTAMP
    WHERE id = OLD.id;
END;
`
