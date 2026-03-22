-- system_config: persistent key-value settings store
CREATE TABLE IF NOT EXISTS system_config (
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    changed_at INTEGER NOT NULL DEFAULT (unixepoch()),
    changed_by TEXT NOT NULL DEFAULT 'system'
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_system_config_key ON system_config(key);

-- system_config_history: audit trail of all changes
CREATE TABLE IF NOT EXISTS system_config_history (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key        TEXT NOT NULL,
    old_value  TEXT,
    new_value  TEXT NOT NULL,
    changed_at INTEGER NOT NULL DEFAULT (unixepoch()),
    changed_by TEXT NOT NULL DEFAULT 'system'
);
CREATE INDEX IF NOT EXISTS idx_system_config_history_key ON system_config_history(key);
