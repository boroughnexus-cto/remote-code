-- Fix FK mismatch between tasks and base_directories.
-- Root cause: base_directory_id is not UNIQUE in base_directories (only the composite
-- (project_id, base_directory_id) is), so SQLite rejects the FK reference from tasks.
-- Fix: recreate base_directories with a standalone UNIQUE on base_directory_id,
-- then rebuild tasks with correct FK metadata referencing the refreshed table.
--
-- PRAGMA foreign_keys=OFF prevents transient FK violations during the rebuild.
PRAGMA foreign_keys=OFF;

-- Rebuild base_directories with standalone UNIQUE on base_directory_id
CREATE TABLE base_directories_031 (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL,
    base_directory_id TEXT NOT NULL,
    path TEXT NOT NULL,
    git_initialized BOOLEAN NOT NULL DEFAULT FALSE,
    setup_commands TEXT NOT NULL DEFAULT '',
    teardown_commands TEXT NOT NULL DEFAULT '',
    dev_server_setup_commands TEXT NOT NULL DEFAULT '',
    dev_server_teardown_commands TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    UNIQUE(base_directory_id),
    UNIQUE(project_id, base_directory_id)
);

INSERT INTO base_directories_031 (id, project_id, base_directory_id, path, git_initialized, setup_commands, teardown_commands, dev_server_setup_commands, dev_server_teardown_commands, created_at, updated_at)
SELECT id, project_id, base_directory_id, path, git_initialized, setup_commands, teardown_commands, dev_server_setup_commands, dev_server_teardown_commands, created_at, updated_at
FROM base_directories;

DROP TABLE base_directories;
ALTER TABLE base_directories_031 RENAME TO base_directories;

CREATE INDEX IF NOT EXISTS idx_base_directories_project_id ON base_directories(project_id);
CREATE INDEX IF NOT EXISTS idx_base_directories_base_directory_id ON base_directories(base_directory_id);

-- Rebuild tasks so its FK metadata points to the refreshed base_directories table
CREATE TABLE tasks_031 (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL,
    base_directory_id TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'todo',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (base_directory_id) REFERENCES base_directories(base_directory_id) ON DELETE CASCADE
);

INSERT INTO tasks_031 (id, project_id, base_directory_id, title, description, status, created_at, updated_at)
SELECT id, project_id, base_directory_id, title, description, status, created_at, updated_at
FROM tasks;

DROP TABLE tasks;
ALTER TABLE tasks_031 RENAME TO tasks;

CREATE INDEX IF NOT EXISTS idx_tasks_project_id ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_tasks_base_directory_id ON tasks(base_directory_id);

PRAGMA foreign_keys=ON;
