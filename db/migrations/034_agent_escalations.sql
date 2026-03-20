-- Pending human-in-the-loop escalations routed through the Telegram hub.
-- DB is the source of truth for correlation (survives SwarmOps restarts).
-- Keyed by Telegram chat_id + message_id for reply-based routing.
CREATE TABLE IF NOT EXISTS agent_escalations (
    id            TEXT     NOT NULL PRIMARY KEY,
    agent_id      TEXT     NOT NULL REFERENCES swarm_agents(id) ON DELETE CASCADE,
    task_id       TEXT,
    question      TEXT     NOT NULL,
    tg_chat_id    INTEGER  NOT NULL,
    tg_message_id INTEGER  NOT NULL,
    created_at    INTEGER  NOT NULL DEFAULT (unixepoch()),
    expires_at    INTEGER  NOT NULL,
    answered_at   INTEGER,
    answer        TEXT
);

-- Fast lookup when a human replies: find the escalation by the message being replied to.
CREATE INDEX IF NOT EXISTS idx_escalations_active
    ON agent_escalations(tg_chat_id, tg_message_id)
    WHERE answered_at IS NULL;
