-- Link swarm sessions to a session context template.
-- context_id is nullable; NULL means the session has no context assigned.
ALTER TABLE swarm_sessions ADD COLUMN context_id TEXT REFERENCES session_contexts(id) ON DELETE SET NULL;
