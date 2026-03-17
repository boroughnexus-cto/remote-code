-- Plane adapter: link goals to Plane issues and track sync state
ALTER TABLE swarm_goals ADD COLUMN plane_issue_id TEXT;
ALTER TABLE swarm_goals ADD COLUMN plane_synced_at INTEGER;
CREATE UNIQUE INDEX IF NOT EXISTS idx_swarm_goals_plane_id
    ON swarm_goals(plane_issue_id) WHERE plane_issue_id IS NOT NULL;

-- Idempotency guards for server-side injections
ALTER TABLE swarm_tasks ADD COLUMN peer_review_injected INTEGER NOT NULL DEFAULT 0;
ALTER TABLE swarm_tasks ADD COLUMN needs_review_count INTEGER NOT NULL DEFAULT 0;
