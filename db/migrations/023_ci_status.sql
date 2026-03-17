ALTER TABLE swarm_tasks ADD COLUMN ci_status TEXT;
ALTER TABLE swarm_tasks ADD COLUMN ci_run_url TEXT;
ALTER TABLE swarm_tasks ADD COLUMN ci_checked_at INTEGER;
ALTER TABLE swarm_tasks ADD COLUMN ci_last_notified_status TEXT;
