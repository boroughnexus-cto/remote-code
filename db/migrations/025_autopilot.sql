-- Per-session autopilot: enables Plane→goal sync for this session
ALTER TABLE swarm_sessions ADD COLUMN autopilot_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE swarm_sessions ADD COLUMN autopilot_plane_project_id TEXT;

-- Obsidian note path recorded when goal documentation is auto-written
ALTER TABLE swarm_goals ADD COLUMN obsidian_note_path TEXT;

-- Idempotency guard for auto-deploy: prevents duplicate Komodo calls on repeated CI polls
ALTER TABLE swarm_tasks ADD COLUMN deploy_triggered_at INTEGER;
