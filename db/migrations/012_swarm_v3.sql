-- Add API token to swarm sessions for orchestrator agent authentication
ALTER TABLE swarm_sessions ADD COLUMN api_token TEXT;
