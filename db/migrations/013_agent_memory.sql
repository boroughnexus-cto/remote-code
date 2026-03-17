-- Agent notes: persistent memory written by orchestrator or user
CREATE TABLE IF NOT EXISTS swarm_agent_notes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id   TEXT    NOT NULL,
    session_id TEXT    NOT NULL,
    content    TEXT    NOT NULL,
    created_by TEXT    NOT NULL DEFAULT 'orchestrator',
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_swarm_agent_notes_agent ON swarm_agent_notes(agent_id, created_at DESC);
