-- 036_batch3.sql
-- Batch 3: per-agent permission toggle, per-task deadline, Plane task write-back

-- SWM-16: per-agent dangerously_skip_permissions toggle
-- DEFAULT 1 preserves existing behaviour (all agents launched with --dangerously-skip-permissions).
ALTER TABLE swarm_agents ADD COLUMN dangerously_skip_permissions INTEGER NOT NULL DEFAULT 1;

-- SWM-7: per-task deadline (Unix epoch seconds, NULL = no per-task deadline)
ALTER TABLE swarm_tasks ADD COLUMN timeout_at INTEGER;

-- SWM-26: Plane task write-back
ALTER TABLE swarm_tasks ADD COLUMN plane_issue_id TEXT;
ALTER TABLE swarm_tasks ADD COLUMN plane_synced_at INTEGER;
