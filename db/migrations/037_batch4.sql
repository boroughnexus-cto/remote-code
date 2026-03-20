-- 037_batch4.sql
-- Batch 4: task dependency graph, session token budget, capability advertising

-- SWM-23: task dependency graph
-- depends_on: JSON array of task IDs this task must wait for before accepting.
-- required_capabilities: comma-separated list of capabilities an agent must have.
ALTER TABLE swarm_tasks ADD COLUMN depends_on TEXT;
ALTER TABLE swarm_tasks ADD COLUMN required_capabilities TEXT;

-- SWM-9: agent capability advertising
-- capabilities: comma-separated list (e.g. "python,docker,testing").
ALTER TABLE swarm_agents ADD COLUMN capabilities TEXT;

-- SWM-8: session-level token circuit breaker
-- Mirrors the per-goal budget pattern at session scope.
-- token_budget=0 means unlimited (same as goals).
ALTER TABLE swarm_sessions ADD COLUMN token_budget  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE swarm_sessions ADD COLUMN tokens_used   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE swarm_sessions ADD COLUMN budget_warning_sent INTEGER NOT NULL DEFAULT 0;
