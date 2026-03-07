-- Execution milestones: stores structured activity summaries extracted
-- from tmux output by the MCP server's milestone tracking system.
CREATE TABLE IF NOT EXISTS execution_milestones (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    execution_id INTEGER NOT NULL REFERENCES task_executions(id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_milestones_execution_id ON execution_milestones(execution_id);
