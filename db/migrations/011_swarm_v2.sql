-- Add repo_path to swarm_agents for worktree spawning
ALTER TABLE swarm_agents ADD COLUMN repo_path TEXT;
