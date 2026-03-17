-- Remove per-session API tokens; localhost connections bypass auth instead
-- SQLite doesn't support DROP COLUMN before 3.35, so we recreate the table
CREATE TABLE swarm_sessions_new (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
INSERT INTO swarm_sessions_new (id, name, created_at, updated_at)
    SELECT id, name, created_at, updated_at FROM swarm_sessions;
DROP TABLE swarm_sessions;
ALTER TABLE swarm_sessions_new RENAME TO swarm_sessions;
