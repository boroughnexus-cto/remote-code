-- Swarm orchestrator tables: sessions, agents, tasks, events

CREATE TABLE IF NOT EXISTS swarm_sessions (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS swarm_agents (
    id              TEXT    PRIMARY KEY,
    session_id      TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    role            TEXT    NOT NULL DEFAULT 'worker',
    worktree_path   TEXT,
    tmux_session    TEXT,
    project         TEXT,
    status          TEXT    NOT NULL DEFAULT 'idle',
    current_file    TEXT,
    current_task_id TEXT,
    created_at      INTEGER NOT NULL,
    FOREIGN KEY (session_id) REFERENCES swarm_sessions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS swarm_tasks (
    id            TEXT    PRIMARY KEY,
    session_id    TEXT    NOT NULL,
    title         TEXT    NOT NULL,
    description   TEXT,
    stage         TEXT    NOT NULL DEFAULT 'spec',
    agent_id      TEXT,
    project       TEXT,
    branch        TEXT,
    worktree_path TEXT,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    FOREIGN KEY (session_id) REFERENCES swarm_sessions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS swarm_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT    NOT NULL,
    agent_id   TEXT,
    task_id    TEXT,
    type       TEXT    NOT NULL,
    payload    TEXT,
    ts         INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_swarm_agents_session ON swarm_agents(session_id);
CREATE INDEX IF NOT EXISTS idx_swarm_tasks_session  ON swarm_tasks(session_id);
CREATE INDEX IF NOT EXISTS idx_swarm_events_session ON swarm_events(session_id);
