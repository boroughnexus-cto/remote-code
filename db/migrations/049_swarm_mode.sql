-- Migration 049: per-agent swarm mode flag
-- When set, the agent is launched with --swarm (Claude Code built-in agent spawning).
ALTER TABLE swarm_agents ADD COLUMN swarm_mode INTEGER NOT NULL DEFAULT 0;
