-- Task bus: durable pull-model task queue for cross-session communication.
-- TKN-146

CREATE TABLE bus_tasks (
    id          TEXT PRIMARY KEY,
    session_id  TEXT,                   -- target session (NULL = unrouted)
    agent_id    TEXT,                   -- set when accepted
    sender_id   TEXT NOT NULL,          -- creator (session ID, 'api', 'dispatcher', etc.)
    source      TEXT,                   -- 'dispatcher', 'pa', 'user', 'api', 'n8n'
    type        TEXT NOT NULL,          -- 'goal', 'inject', 'query', 'notification'
    payload     TEXT NOT NULL,          -- JSON
    state       TEXT NOT NULL DEFAULT 'pending'
                    CHECK (state IN ('pending','accepted','deferred','rejected','completed','failed','expired')),
    priority    INTEGER NOT NULL DEFAULT 5
                    CHECK (priority BETWEEN 1 AND 10),
    external_id TEXT UNIQUE,            -- caller dedup key (e.g. Plane issue ID, email msgid)
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    accepted_at DATETIME,
    resolved_at DATETIME,
    defer_until DATETIME,               -- set when state='deferred'
    ttl_seconds INTEGER                 -- NULL = no expiry
);

-- inbox query: pending tasks for a session ordered by urgency
CREATE INDEX idx_tasks_inbox    ON bus_tasks(session_id, state, priority, created_at);
-- sweeper: find expired pending tasks and tasks to re-queue
CREATE INDEX idx_tasks_sweeper  ON bus_tasks(state, defer_until);
CREATE INDEX idx_tasks_ttl      ON bus_tasks(state, ttl_seconds, created_at);

CREATE TABLE bus_task_events (
    id       TEXT PRIMARY KEY,
    task_id  TEXT NOT NULL REFERENCES bus_tasks(id),
    agent_id TEXT,
    event    TEXT NOT NULL
                 CHECK (event IN ('created','accepted','rejected','deferred','completed','failed','expired','requeued')),
    reason   TEXT,
    ts       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_task_events_task ON bus_task_events(task_id);
