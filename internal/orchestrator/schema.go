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
