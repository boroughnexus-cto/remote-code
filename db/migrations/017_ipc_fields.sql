ALTER TABLE swarm_agents ADD COLUMN inbox_path TEXT;
ALTER TABLE swarm_agents ADD COLUMN outbox_path TEXT;
ALTER TABLE swarm_agents ADD COLUMN last_event_offset INTEGER DEFAULT 0;
ALTER TABLE swarm_agents ADD COLUMN last_event_ts INTEGER DEFAULT 0;
ALTER TABLE swarm_tasks ADD COLUMN message_id TEXT;
ALTER TABLE swarm_tasks ADD COLUMN accepted_at INTEGER;
