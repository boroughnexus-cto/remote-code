ALTER TABLE swarm_tasks ADD COLUMN started_at INTEGER;
ALTER TABLE swarm_tasks ADD COLUMN completed_at INTEGER;
ALTER TABLE swarm_tasks ADD COLUMN confidence REAL;
ALTER TABLE swarm_tasks ADD COLUMN tokens_used INTEGER;
ALTER TABLE swarm_tasks ADD COLUMN blocked_reason TEXT;
