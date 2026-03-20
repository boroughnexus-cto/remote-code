-- Per-run ephemeral state for agents.
-- Ephemeral transport/session state lives here, not in swarm_agents (stable identity).
-- Each agent spawn creates a row; ended_at is set when the agent terminates.
CREATE TABLE IF NOT EXISTS agent_runs (
    run_id         TEXT     NOT NULL PRIMARY KEY,
    agent_id       TEXT     NOT NULL REFERENCES swarm_agents(id) ON DELETE CASCADE,
    channels_url   TEXT,
    run_token      TEXT     NOT NULL DEFAULT '', -- unguessable per-run secret for SSE auth; always set at spawn
    transport_mode TEXT     NOT NULL DEFAULT 'tmux'
                            CHECK (transport_mode IN ('tmux', 'channels', 'shadow', 'canary')),
    started_at     INTEGER  NOT NULL DEFAULT (unixepoch()),
    acked_at       INTEGER, -- unix timestamp of last successful channels message ack
    ended_at       INTEGER
);

CREATE INDEX IF NOT EXISTS idx_agent_runs_agent_id ON agent_runs(agent_id);

-- Enforce at most one active run per agent at a time.
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runs_active
    ON agent_runs(agent_id) WHERE ended_at IS NULL;
