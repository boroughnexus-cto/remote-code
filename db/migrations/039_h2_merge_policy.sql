-- 039_h2_merge_policy.sql
-- H2: Per-session merge policy for auto-merging PRs after judge passes.

-- Merge policy on session: 'manual' | 'confidence' | 'always'
-- 'manual'     — never auto-merge (default, preserves existing behaviour)
-- 'confidence' — auto-merge when judge_confidence >= merge_confidence_threshold AND CI passes
-- 'always'     — auto-merge whenever CI passes
ALTER TABLE swarm_sessions ADD COLUMN merge_policy TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE swarm_sessions ADD COLUMN merge_confidence_threshold REAL NOT NULL DEFAULT 0.85;

-- PR tracking on tasks
ALTER TABLE swarm_tasks ADD COLUMN pr_status TEXT;
-- valid values: 'open' | 'ready_for_review' | 'merged' | 'closed'

-- Idempotency guard: prevents reconcile loop from re-attempting a merge
-- that was already dispatched. Set BEFORE dispatching to prevent races.
ALTER TABLE swarm_tasks ADD COLUMN last_merge_attempt_at INTEGER;
