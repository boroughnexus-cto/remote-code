-- Store the Claude model assigned to a session at spawn time.
ALTER TABLE managed_sessions ADD COLUMN model TEXT;
