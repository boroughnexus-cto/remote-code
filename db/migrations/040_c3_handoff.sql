-- 040_c3_handoff.sql
-- C3: Structured handoff table for context rotation continuations.
-- Replaces the implicit JSON blob approach — typed columns for queryability
-- and foreign key integrity.

CREATE TABLE IF NOT EXISTS swarm_task_handoffs (
    task_id             TEXT PRIMARY KEY,
    context_summary     TEXT NOT NULL DEFAULT '',
    current_diff_ref    TEXT NOT NULL DEFAULT '',  -- branch name for git diff
    next_steps          TEXT NOT NULL DEFAULT '[]', -- JSON array of strings
    decisions_log       TEXT NOT NULL DEFAULT '[]', -- JSON array of strings
    failing_tests       TEXT NOT NULL DEFAULT '[]', -- JSON array of strings
    acceptance_criteria TEXT NOT NULL DEFAULT '[]', -- JSON array of strings
    confidence          REAL NOT NULL DEFAULT 0.0,
    created_at          INTEGER NOT NULL DEFAULT (unixepoch()),
    extra_json          TEXT,                        -- forward-compatible overflow
    FOREIGN KEY (task_id) REFERENCES swarm_tasks(id) ON DELETE CASCADE
);

-- requeued_at: idempotency guard for emergencyRotateAgent re-queue.
-- Set atomically when task transitions needs_review → queued.
-- Prevents duplicate re-queuing on crash/retry.
ALTER TABLE swarm_tasks ADD COLUMN requeued_at INTEGER;
