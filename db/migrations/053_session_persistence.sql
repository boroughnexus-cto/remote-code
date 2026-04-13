-- Add Claude session ID for conversation resume across restarts.
ALTER TABLE managed_sessions ADD COLUMN claude_session_id TEXT;
