CREATE TABLE IF NOT EXISTS swarm_artifacts (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    task_id TEXT,
    goal_id TEXT,
    agent_id TEXT,
    type TEXT NOT NULL,
    path TEXT NOT NULL,
    hash TEXT,
    summary TEXT,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS swarm_decisions (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
