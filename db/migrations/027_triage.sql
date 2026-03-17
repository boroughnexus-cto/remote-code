-- Batch 5: Proactive goal discovery — triage agent

-- Per-session opt-in: triage_enabled=1 means this session receives auto-created triage goals.
ALTER TABLE swarm_sessions ADD COLUMN triage_enabled INTEGER NOT NULL DEFAULT 0;

-- Triage findings: one row per unique signal (fingerprint). UPSERT keeps last_seen_at current.
CREATE TABLE IF NOT EXISTS swarm_triage_findings (
    id                   TEXT PRIMARY KEY,
    session_id           TEXT NOT NULL,
    fingerprint          TEXT NOT NULL,           -- dedup key; UNIQUE across all sessions
    signal_type          TEXT NOT NULL,           -- stale_pr | vuln | ci_failure
    repo_path            TEXT NOT NULL,
    title                TEXT NOT NULL,
    detail               TEXT NOT NULL,
    goal_id              TEXT,                    -- most recent goal created for this finding
    status               TEXT NOT NULL DEFAULT 'open', -- open | suppressed
    first_seen_at        INTEGER NOT NULL,
    last_seen_at         INTEGER NOT NULL,
    last_goal_created_at INTEGER,                 -- cooldown anchor: when we last created a goal
    suppressed_at        INTEGER
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_triage_fp      ON swarm_triage_findings(fingerprint);
CREATE        INDEX IF NOT EXISTS idx_triage_session ON swarm_triage_findings(session_id, last_seen_at DESC);
CREATE        INDEX IF NOT EXISTS idx_triage_repo    ON swarm_triage_findings(repo_path, signal_type, last_seen_at);
