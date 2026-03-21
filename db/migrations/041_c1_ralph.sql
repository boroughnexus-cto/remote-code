-- 041_c1_ralph.sql
-- C1: Ralph rework loop termination — terminal states + escalation package.

-- terminal_reason on tasks: human-readable explanation of why the task
-- entered a terminal state (failed_ralph_loop, needs_human, etc).
ALTER TABLE swarm_tasks ADD COLUMN terminal_reason TEXT;

-- terminal_reason on goals: mirrors task terminal_reason at goal scope.
ALTER TABLE swarm_goals ADD COLUMN terminal_reason TEXT;

-- needs_human on goals: new status value for goals blocked waiting on human input.
-- swarm_goals.status already exists (from 021_goals.sql) — value is enforced
-- in application layer only; no CHECK constraint possible without table rebuild.
-- Valid values after this migration: active | complete | cancelled | failed | needs_human

-- escalation_type on agent_escalations: classifies the escalation category
-- so the Telegram router can dispatch to the right handler.
-- valid values: 'question' | 'ralph_loop' | 'budget' | 'conflict'
ALTER TABLE agent_escalations ADD COLUMN escalation_type TEXT NOT NULL DEFAULT 'question';

-- goal_id on agent_escalations: links a ralph_loop or budget escalation
-- to its parent goal for option-reply routing.
ALTER TABLE agent_escalations ADD COLUMN goal_id TEXT;
