CREATE TABLE IF NOT EXISTS swarm_goals (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    description TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_swarm_goals_session ON swarm_goals(session_id, status);
ALTER TABLE swarm_tasks ADD COLUMN goal_id TEXT;
CREATE INDEX IF NOT EXISTS idx_swarm_tasks_goal ON swarm_tasks(goal_id);
