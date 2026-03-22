-- Session contexts: per-session instruction blocks injected into agents at spawn.
-- Agents inherit the context of their session; context is shown in the spawn prompt.
CREATE TABLE IF NOT EXISTS session_contexts (
    id          TEXT    NOT NULL PRIMARY KEY DEFAULT (lower(hex(randomblob(8)))),
    name        TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    content     TEXT    NOT NULL,
    tags        TEXT    NOT NULL DEFAULT '',   -- comma-separated
    created_at  INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at  INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE INDEX IF NOT EXISTS idx_session_contexts_name ON session_contexts(name);
