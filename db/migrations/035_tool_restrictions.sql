-- Per-agent tool allow/deny lists passed to Claude Code via --allowedTools / --disallowedTools.
-- Stored as comma-separated tool names. NULL means no restriction.
ALTER TABLE swarm_agents ADD COLUMN allowed_tools TEXT;
ALTER TABLE swarm_agents ADD COLUMN disallowed_tools TEXT;
