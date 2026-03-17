-- Batch 4: Production reliability + intelligence features

-- Task watchdog: track last heartbeat per running task (null = no heartbeat yet)
ALTER TABLE swarm_tasks ADD COLUMN last_heartbeat_at INTEGER;

-- Goal token budget: per-goal spend cap and running total
ALTER TABLE swarm_goals ADD COLUMN token_budget INTEGER NOT NULL DEFAULT 0; -- 0 = unlimited
ALTER TABLE swarm_goals ADD COLUMN tokens_used  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE swarm_goals ADD COLUMN budget_warning_sent INTEGER NOT NULL DEFAULT 0; -- idempotency guard

-- Acceptance criteria judge result (written by judge phase)
ALTER TABLE swarm_goals ADD COLUMN judge_notes TEXT;

-- Goal complexity routing: trivial | standard | complex
ALTER TABLE swarm_goals ADD COLUMN complexity TEXT NOT NULL DEFAULT 'standard';

-- Judge injection idempotency guard (mirrors peer_review_injected)
ALTER TABLE swarm_tasks ADD COLUMN judge_injected INTEGER NOT NULL DEFAULT 0;

-- Index for watchdog query
CREATE INDEX IF NOT EXISTS idx_swarm_tasks_watchdog
    ON swarm_tasks(stage, last_heartbeat_at, started_at)
    WHERE stage IN ('running','accepted');
