-- 042_h1_leases.sql
-- H1: Multi-agent worktree locking via repo-level lease table.
-- Prevents two agents from simultaneously modifying the same repository.

-- Normalised scope declarations: the set of path prefixes an agent
-- claims to modify for a given task.
CREATE TABLE IF NOT EXISTS swarm_task_scopes (
    id          TEXT PRIMARY KEY,
    task_id     TEXT NOT NULL,
    path_prefix TEXT NOT NULL,
    FOREIGN KEY (task_id) REFERENCES swarm_tasks(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_scopes_unique
    ON swarm_task_scopes(task_id, path_prefix);

-- Active repo lease per task.
-- One active lease per task (enforced by partial unique index below).
-- released_at NULL = active; populated = released.
CREATE TABLE IF NOT EXISTS swarm_repo_leases (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL,
    goal_id     TEXT NOT NULL,
    task_id     TEXT NOT NULL,
    repo_path   TEXT NOT NULL,
    acquired_at INTEGER NOT NULL DEFAULT (unixepoch()),
    expires_at  INTEGER NOT NULL, -- acquired_at + 7200 (2h TTL, watchdog cleans up)
    released_at INTEGER,
    FOREIGN KEY (task_id) REFERENCES swarm_tasks(id) ON DELETE CASCADE
);

-- One active lease per task (partial index on released_at IS NULL).
CREATE UNIQUE INDEX IF NOT EXISTS idx_lease_one_per_task
    ON swarm_repo_leases(task_id)
    WHERE released_at IS NULL;

-- Fast lookup for conflict detection: all active leases for a repo.
CREATE INDEX IF NOT EXISTS idx_repo_leases_active
    ON swarm_repo_leases(repo_path, released_at)
    WHERE released_at IS NULL;
