-- Improve event log querying: add composite indexes for agent/task filtering
-- and a rowid-ordered index for chronological retrieval.

CREATE INDEX IF NOT EXISTS idx_swarm_events_session_id  ON swarm_events(session_id, id);
CREATE INDEX IF NOT EXISTS idx_swarm_events_agent       ON swarm_events(session_id, agent_id, id);
CREATE INDEX IF NOT EXISTS idx_swarm_events_task        ON swarm_events(session_id, task_id, id);
CREATE INDEX IF NOT EXISTS idx_swarm_events_type        ON swarm_events(session_id, type, id);
