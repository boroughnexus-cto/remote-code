-- Add summary and dynamic_context to session_contexts.
-- summary:         1-2 line description shown in TUI right panel and picker.
-- dynamic_context: freeform instructions injected into agent CLAUDE.md at spawn,
--                  instructing the agent to fetch up-to-date context before starting.
ALTER TABLE session_contexts ADD COLUMN summary TEXT NOT NULL DEFAULT '';
ALTER TABLE session_contexts ADD COLUMN dynamic_context TEXT NOT NULL DEFAULT '';
