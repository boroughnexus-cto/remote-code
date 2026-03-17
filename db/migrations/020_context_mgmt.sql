ALTER TABLE swarm_agents ADD COLUMN context_pct REAL DEFAULT 0;
ALTER TABLE swarm_agents ADD COLUMN context_state TEXT DEFAULT 'normal';
ALTER TABLE swarm_agents ADD COLUMN rotated_from TEXT;
ALTER TABLE swarm_agents ADD COLUMN rotated_at INTEGER;
