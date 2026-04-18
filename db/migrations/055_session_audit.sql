-- Audit trail for managed Claude Code sessions.
-- Records lifecycle events: created, stopped, deleted, renamed, mission_set.
CREATE TABLE IF NOT EXISTS managed_session_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT    NOT NULL,  -- managed_sessions.id (may be deleted)
    name       TEXT    NOT NULL,  -- session name at time of event
    event_type TEXT    NOT NULL,  -- 'created' | 'stopped' | 'deleted' | 'renamed' | 'mission_set'
    details    TEXT,               -- optional JSON payload
    ts         INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_managed_session_events_ts
    ON managed_session_events(ts DESC);
CREATE INDEX IF NOT EXISTS idx_managed_session_events_session
    ON managed_session_events(session_id, id);
