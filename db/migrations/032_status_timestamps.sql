-- Track when agent status and task stage last changed.
-- NULL means the row predates this migration; treated as "unknown" in the TUI.
ALTER TABLE swarm_agents ADD COLUMN status_changed_at INTEGER;
ALTER TABLE swarm_tasks  ADD COLUMN stage_changed_at INTEGER;
