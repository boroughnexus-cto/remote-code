-- Fleet state persistence: survives server restarts.
-- Single-row table (id is always 1, enforced by CHECK constraint).
CREATE TABLE IF NOT EXISTS fleet_state (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    mode   TEXT    NOT NULL DEFAULT 'normal',
    set_by TEXT    NOT NULL DEFAULT '',
    set_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Seed the singleton row so LoadFromDB can always do a simple SELECT.
INSERT OR IGNORE INTO fleet_state (id, mode, set_by) VALUES (1, 'normal', 'init');
