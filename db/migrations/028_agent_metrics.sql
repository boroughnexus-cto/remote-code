-- Per-agent metrics collected from Claude Code Stop hooks
ALTER TABLE swarm_agents ADD COLUMN model_name TEXT;
ALTER TABLE swarm_agents ADD COLUMN tokens_used INTEGER NOT NULL DEFAULT 0;
