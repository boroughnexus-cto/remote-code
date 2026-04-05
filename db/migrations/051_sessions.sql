-- Session manager: managed Claude Code tmux sessions.
CREATE TABLE IF NOT EXISTS managed_sessions (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    tmux_session TEXT NOT NULL UNIQUE,
    directory    TEXT NOT NULL DEFAULT '.',
    context_id   TEXT,
    context_name TEXT,
    hidden       INTEGER NOT NULL DEFAULT 0,
    status       TEXT NOT NULL DEFAULT 'running',
    created_at   INTEGER NOT NULL DEFAULT 0,
    updated_at   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_managed_sessions_status ON managed_sessions(status);
